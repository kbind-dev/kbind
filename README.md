# kbind

kbind binds APIs exported by a provider cluster into a consumer cluster. The
**slim core** is a consumer-side sync engine (the konnector) consuming a
one-apply bundle; the optional **extended layer** (backend gateway + issuer +
reaper, CLI, UI) produces that bundle for humans. See
[docs/proposals/v2-slim-core.md](docs/proposals/v2-slim-core.md) and
[docs/proposals/v2-extended.md](docs/proposals/v2-extended.md) for the designs.

## Layout

```
.                          # module: github.com/kbind/kbind (konnector + service layer)
├── go.work                # ties the two modules for local dev
├── cmd/konnector/         # main: local mgr + mcmanager + reconcilers
├── cmd/backend/           # main: kbind-backend (gateway/issuer/reaper module flags)
├── cmd/bind/              # main: the bind CLI (login, catalog, bind an export)
├── engine/
│   ├── provider/          # mcr provider: Connection -> engaged cluster
│   ├── connection/        # resolve secret, pin identity, discover exports
│   ├── binding/           # validate + pull CRDs (schema.source: CRD)
│   ├── sync/              # per-GVR spec-up / status-down + conflicts
│   └── remote/            # kubeconfig + cluster identity helpers
├── backend/
│   ├── auth/              # Authenticator iface, OIDC (+ --oidc-mock), sessions
│   ├── issuer/            # Issuer iface + kube impl, Grant/Export controllers, bundle
│   ├── gateway/           # HTTP API: catalog, bind, one-time bundle pickup, apply
│   └── reaper/            # Lease-keyed GC of stale Grants
├── cli/                   # bind CLI packages (client, login dance, commands)
├── web/                   # embedded gateway UI (dependency-free SPA)
├── pkg/
│   ├── kubeapply/         # SSA apply helper for bundles/manifests
│   └── konnectorinstall/  # embedded konnector install (CRDs, RBAC, Deployment)
├── test/e2e/              # two-envtest end-to-end suite (core + backend loop)
├── deploy/charts/konnector/  # consumer-side Helm chart (CRDs, RBAC, Deployment)
├── deploy/charts/backend/    # provider-side Helm chart (service layer)
├── sdk/                   # module: github.com/kbind/kbind/sdk (type-only API)
│   ├── apis/core/v1alpha1/    #   Connection, ClusterBinding, Binding (core.kbind.io)
│   ├── apis/catalog/v1alpha1/ #   Export, Collection (catalog.kbind.io)
│   ├── apis/iam/v1alpha1/     #   Grant (iam.kbind.io)
│   └── config/crd/            #   generated CRD manifests
└── hack/demo.sh           # two-kind-cluster end-to-end demo
```

## What works today (POC milestone: E2E single-API sync)

- `Connection` resolves a kubeconfig Secret, pins provider/consumer cluster
  identity, and discovers label-gated exported CRDs into `status.exportedAPIs`.
- `ClusterBinding` / `Binding` validate the connection, pull the listed CRDs
  (`schema.source: CRD`, single served version, no conversion webhook) onto the
  consumer, and report `Ready` + `boundAPIs`.
- A dynamic per-GVR syncer copies instance **spec up** (server-side apply with
  ownership markers + a finalizer) and **status down**. `conflictPolicy: Fail`
  refuses a foreign provider target (Event + conflict annotation, counted on the
  binding's `conflictCount` + `Conflicts` condition); `conflictPolicy: Adopt`
  takes over an *un-owned* provider object (never one owned by another binding).
- The Connection **re-discovers** exported APIs periodically, so a CRD labeled
  `exported` after connect is picked up and its binding goes Ready.
- Schema knobs are honored: `pullPolicy: Bound`/`All`/`None`, `updatePolicy:
  Always`/`Once`, and `autoBind` (a managed ClusterBinding mirroring exported
  APIs). `deletion-policy: Orphan` keeps a provider copy on delete/unbind.
  Provider RBAC denials surface as a `PermissionDenied` condition / Event.
- `schema.source: OpenAPI` (and `Auto`) synthesizes the consumer CRD from the
  provider's discovery + `/openapi/v3` — the CRD-less (kcp-like) path — and the
  Connection installs it. Known fidelity limits (CEL, defaulting, `$ref`,
  multi-version) are accepted; the provider stays the enforcing side.
- The konnector maintains a `coordination.k8s.io/Lease` per Connection on the
  provider (heartbeat) — the hook a service-layer reaper keys off.
- **kcp-aware cluster identity**: the provider's stable identity is the kcp
  `LogicalCluster` ("cluster") UID when present (a kcp workspace serves
  `core.kcp.io`, and has no `kube-system`), falling back to the `kube-system`
  namespace UID on plain Kubernetes. A provider RBAC denial on the identity read
  surfaces as `PermissionDenied`.
- `relatedResources` sync selected Secrets/ConfigMaps in the declared direction,
  scoped like the binding, GC'd when they stop matching or on unbind.
- The provider side is the **multicluster-runtime engaged cluster** for each
  Connection: writes go through its client, fresh reads through its API reader,
  and status/drift events arrive via a **watch on its cache** (event-driven, not
  polled — a low-frequency resync is only a backstop).
- **Stop-on-disengage**: a Connection that loses readiness (revoked credential,
  unreachable provider, withdrawn RBAC) is disengaged, and its per-GVR syncers
  are torn down rather than left running against a dead cluster. When it becomes
  Ready again the provider re-engages as a fresh cluster and the syncers are
  rebuilt against it (a stale syncer would otherwise hold a dead client forever).
- **Mapper extension point** (`engine/mapper`): the syncer routes every
  provider-side operation through a `Mapper` that translates the consumer object
  key to its provider key. Core ships only `Identity` (ns/name unchanged); an
  out-of-tree build supplies its own via `sync.WithMapper(...)` to restore v1's
  "Prefixed" key isolation without forking the engine. The interface maps keys
  only — it cannot change scope (cluster-scoped stays cluster-scoped), and it is
  deliberately kept out of the CRD API so the core API never promises renaming.
- **Order-independent apply**: a `Connection` created before its Secret resolves
  when the Secret arrives (the konnector watches referenced Secrets); a binding
  created before its Connection resolves when the Connection goes Ready.
- **Complete unbind**: `Connection` and bindings carry a cleanup finalizer.
  Deleting a `ClusterBinding` deletes the provider copies of synced instances,
  releases instance finalizers, and removes the pulled CRD (cascading the
  instances). A `Connection` blocks (`DrainingBindings`) until its bindings are
  gone, and keeps its Secret alive (via a finalizer) so teardown can still reach
  the provider — so `kubectl delete -f bundle.yaml` is order-don't-care.

Known POC simplifications (tracked against the proposal): OpenAPI synthesis is
best-effort (fidelity limits above); the `Mapper` seam exists but only `Identity`
ships and `relatedResources` are not yet routed through it.

## The extended layer (backend, CLI, UI)

The service layer is optional — GitOps against the core objects works without
any of it. It answers what the core deliberately doesn't: who are you (OIDC),
what may you have (catalog), here are your credentials (issuer), here is your
bundle (gateway), you stopped coming (reaper).

- **One binary, module flags**: `kbind-backend` runs on/against the **provider**
  cluster with `--enable-gateway` (HTTP API + UI), `--enable-issuer` (Grant
  provisioning + catalog validation), `--enable-reaper` (off by default) and
  `--enable-apply` (browser-apply, off by default).
- **Catalog**: curate offerings as `Export`/`Collection` (`catalog.kbind.io`)
  on the provider; an Export listing a non-exported API is hidden until the
  `core.kbind.io/exported` label appears. An Export's
  `defaults.relatedResources` (the core's secrets/configmaps-alongside-the-API
  concept) flow into the Grant, the issued RBAC and the bundle's
  ClusterBinding — and are shown on the catalog card (`⇩ secrets`-style chips)
  so consumers see what will flow before binding.
- **Issuance**: every bind records a `Grant` (`iam.kbind.io`) — identity,
  export, resolved APIs — and the issuer provisions a boundary namespace, a
  ServiceAccount with RBAC enumerating exactly the granted APIs
  (`--issuer-scope Cluster|Namespace`), and a long-lived SA token. Deleting the
  Grant revokes. The issued kubeconfig pins its context namespace to the
  boundary, which is where the konnector's heartbeat Lease lands — the reaper's
  signal.
- **Clusters view**: `GET /api/clusters` (UI tab "Clusters", CLI
  `kubectl bind clusters`) aggregates the konnector heartbeat Leases into a
  consumer-cluster inventory — which clusters are live, what each has bound
  (grant/export/identity, heartbeat age) and how many objects it is syncing
  per API (counted via the sync engine's ownership markers). Grants whose
  bundle was never applied show up as "bound, never connected".
- **Catalog instances**: `GET /api/catalog/<export>/instances` (an expandable
  "Synced instances" panel on each catalog item; CLI `kubectl bind instances
  <export>`) lists the provider-side objects consumers synced under an
  offering — namespace/name, owning cluster, identity (via the heartbeat
  Lease → Grant link) and age.
- **Connect a cluster**: `GET /api/konnector` serves the konnector install
  (CRDs, RBAC, Deployment) as one apply-able YAML — no auth, no credentials
  inside, so `curl -fsS <gateway>/api/konnector | kubectl apply -f -` onboards
  a cluster. Surfaced as a "Connect a cluster" button in the Clusters view
  (with a gateway-side install when `--enable-apply` is on) and as
  `kubectl bind connect` in the CLI.
- **Bundle delivery**: `POST /api/bind` returns a **one-time pickup URL**
  (5-minute TTL); the pickup itself is single-use, the credentials inside stay
  valid until revoked (GitOps-safe). The gateway is stateless — sessions are
  encrypted cookies/bearer tokens, single-use is enforced through annotations
  on the Grant — so replicas just need shared `--cookie-*-key`s.
- **Auth**: OIDC with kube-apiserver-style flags (`--oidc-issuer-url`,
  `--oidc-client-id`, `--oidc-client-secret`, `--oidc-username-claim`,
  `--oidc-groups-claim`, `--oidc-scopes`, `--oidc-ca-file`,
  `--oidc-redirect-url`), or `--oidc-mock` for a dev issuer that auto-approves.
  `--kubernetes-auth` additionally accepts provider-cluster bearer tokens
  (verified via TokenReview) — for in-platform callers that already hold a
  cluster identity and shouldn't need a second SSO round trip.

The CLI installs via krew under the plugin name **`bind`** (kept from v1):
`kubectl krew install bind`, then `kubectl bind …` — or grab the `kbind`
binary from a release / `make bind`.

```sh
# dev loop against the current kubeconfig context as the provider:
go run ./cmd/backend --oidc-mock --external-url http://localhost:8080

kubectl bind login http://localhost:8080     # browser dance, caches the session
kubectl bind connect                         # install the konnector into your cluster
kubectl bind catalog                         # list Exports/Collections
kubectl bind export mangodb             # bundle → konnector install → apply
kubectl bind export mangodb -o yaml > b.yaml  # GitOps mode: print, don't apply
kubectl bind clusters                        # which consumer clusters sync what
# ...or no CLI at all:
curl -fsS <pickup-url> | kubectl apply -f -
```

The UI (embedded in the gateway, `/`) browses the catalog, binds, and hands
out the bundle ticket; browser-apply appears only when `--enable-apply` is on.

## Build

```sh
make build            # builds both modules (workspace mode via go.work)
make konnector        # builds the konnector binary into ./bin
make backend          # builds the kbind-backend binary into ./bin
make bind             # builds the bind CLI into ./bin
```

## Deploy

The konnector runs in (or against) the **consumer** cluster — it is the only
running component of the core (no backend, no provider-side controllers).

```sh
make image IMAGE=ghcr.io/kbind/konnector:dev          # build the image
helm install konnector deploy/charts/konnector \
  -n kbind --create-namespace \
  --set image.repository=ghcr.io/kbind/konnector --set image.tag=dev
```

The chart ([deploy/charts/konnector](deploy/charts/konnector)) ships the core
CRDs (`installCRDs`, default on), a `ServiceAccount`, the consumer RBAC
(`ClusterRole`/`ClusterRoleBinding` + a namespaced leader-election `Role`), and
the `Deployment` with liveness/readiness probes. Notable values:

- `replicaCount` / `leaderElect` — HA. Leader election gates all consumer-side
  controllers, so standby replicas engage no providers until they win the lease.
  It is forced on automatically when `replicaCount > 1`.
- `rbac.boundResourceGroups` (default `["*"]`) — the API groups the konnector may
  sync. The bound APIs are open-ended, so this defaults to all groups; narrow it
  to the specific groups your providers export to shrink the blast radius.

Provider credentials are governed by the kubeconfig in each `Connection`'s
Secret, **not** the konnector's ServiceAccount — the RBAC above is consumer-side
only.

The **backend** deploys on the provider with its own chart
([deploy/charts/backend](deploy/charts/backend)):

```sh
make image-backend BACKEND_IMAGE=ghcr.io/kbind/backend:dev
helm install backend deploy/charts/backend \
  -n kbind-system --create-namespace \
  --set externalURL=https://kbind.example.com \
  --set oidc.issuerURL=https://sso.example.com \
  --set oidc.clientID=kbind --set oidc.existingSecret=kbind-oidc \
  --set cookieKeys.existingSecret=kbind-cookie-keys
```

Module flags map to `modules.{gateway,issuer,reaper,apply}` values; the chart
ships the catalog/iam CRDs, a Service for the gateway, and RBAC that includes
`escalate`/`bind` on Roles so the issuer may mint tenant Roles enumerating APIs
the backend itself does not hold. For a dev install, `--set oidc.mock=true` is
the only required auth setting.

## Codegen (after editing types)

```sh
make codegen          # regenerates deepcopy + CRDs under sdk/
make helm-sync-crds   # codegen + refresh the chart's bundled CRDs
```

## Tests

The end-to-end test ([test/e2e](test/e2e)) runs two in-process **envtest** API
servers (provider + consumer) with the real engine reconcilers: Connection Ready
+ discovery → ClusterBinding Ready + CRD pull → spec up → status down → spec
update → conflict (foreign object not overwritten) → deletion.
`backend_test.go` closes the extended-layer loop on the same harness: catalog →
gateway bind → issuer-provisioned credentials → one-time pickup → one apply →
sync through the issued RBAC-fenced ServiceAccount → heartbeat in the boundary
namespace → reaper → revocation.

```sh
make test             # unit tests (no external setup)
make test-e2e         # downloads envtest assets and runs the e2e suite
```

## Run the demo (two kind clusters)

```sh
make demo             # creates two kind clusters and wires the bundle
# then follow the printed instructions to run the konnector and sync a Widget
```

## Tilt dev loop (two kind clusters)

Tilt drives one kube-context per Tiltfile, so the two-cluster loop is split:
the **provider** cluster (backend image + chart, port-forwards, rebuild on
change) is managed natively, and the **consumer** cluster (konnector image +
chart) is driven through `local_resource` steps against the second context —
one `tilt up`, both clusters in one UI.

```sh
make tilt                    # kind clusters + backend (mock OIDC) + seeded catalog + konnector
make tilt-down               # tear both clusters down
```

Then: open http://localhost:8080 (UI, login auto-approves via the mock
issuer — its fixed in-pod port 5556 is forwarded so the browser can reach it),
or `kubectl bind login http://localhost:8080` and
`kubectl bind export widgets --kubeconfig <consumer kubeconfig>`. Issued kubeconfigs
point at `kbind-provider-control-plane:6443`, reachable from consumer pods on
the shared kind docker network.
