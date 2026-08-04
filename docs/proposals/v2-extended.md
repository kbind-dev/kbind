# kbind v2 Extended: Backend API, CLI, UI

* Status: **ACCEPTED — in implementation**
* Authors: @mjudeikis
* Date: 2026-06-10 (updated 2026-07-29 for the kbind rename + root-layout restructure)
* Builds on: [v2-slim-core.md](v2-slim-core.md) (implemented on the `v2-next` branch)

> **2026-07-29 update.** The slim core shipped under a new identity: the project is
> **kbind** (`github.com/kbind/kbind`, API group `core.kbind.io`), the `v2/` prefix is
> gone (the konnector module IS the repo root, v1 was deleted). Everything below is
> normalized to that reality: groups are `catalog.kbind.io` / `iam.kbind.io`, the CLI is
> `kbind`, the backend binary is `kbind-backend`, and the Packaging section describes the
> root layout. Additional implementation decisions are logged in **Decided**.

## Summary

The v2 core contract is up for discussion: a binding is one `kubectl apply` of a Secret +
`Connection` + bindings, consumed by the konnector, with zero kube-bind components on
the provider. This proposal designs everything *around* that contract — the optional
service layer that answers the questions the core deliberately doesn't:

* **Who are you?** (auth: OIDC, sessions)
* **What may you have?** (catalog: curated offerings on top of raw exported APIs)
* **Here are your credentials.** (issuer: per-consumer SA/RBAC/kubeconfig, tenancy)
* **Here is your bundle.** (gateway: HTTP API whose terminal output is the one-apply file)
* **You stopped coming.** (reaper: GC keyed off the core's Lease)
* **Make it pleasant.** (CLI, UI)

The defining rule, inherited from the core: **every path through this layer terminates
in the same one-apply bundle.** The service layer negotiates; it never syncs. If the
backend is deleted the day after binding, sync is unaffected.

```
            provider cluster                                consumer cluster
 ┌────────────────────────────────────┐
 │  extended layer (this proposal)    │
 │  ┌──────────┐ ┌────────┐ ┌──────┐  │     bundle
 │  │ gateway  │ │ issuer │ │reaper│  │  (one-apply file)      ┌───────────┐
 │  │ auth, UI │ │ creds, │ │ Lease│  │ ──────────────────────▶│ konnector │
 │  │ catalog  │ │ tenancy│ │  GC  │  │   via CLI / UI / GitOps│  (core)   │
 │  └──────────┘ └────────┘ └──────┘  │                        └─────┬─────┘
 └────────────────────────────────────┘                              │
            ▲          sync (core contract: CRDs, spec ⇧, status ⇩) │
            └────────────────────────────────────────────────────────┘
```

## Goals

* Every component independently deployable and optional; any subset works. The core
  (GitOps-only, no extended layer at all) remains a first-class path forever.
* The backend's terminal output is **exactly** the core's one-apply bundle — no
  intermediate request/response CRDs on the consumer, no phase-gated handshake.
* Tenancy lives here: per-consumer provider namespaces / kcp workspaces are an issuer
  concern, invisible to the core (which just sees a kubeconfig whose RBAC fences it in).
* Pluggable auth from day one — OIDC is the reference implementation, not the contract.
* HA-capable by construction (the v1 in-memory-session/single-replica limitation,
  roadmap #424/#488, must not survive into v2).

## Non-Goals

* Anything that changes core sync semantics — the core contract is immutable, this layer must adapt around it.
* Marketplace/billing/quotas (a future layer above this one; the catalog leaves room).
* Re-implementing v1's wire protocol (`BindingProvider`, `BindingResourceResponse`,
  `APIServiceExportRequest` flow). v2 extended is a clean protocol.

## Components

### 1. Catalog (provider-side CRDs)

Raw discovery already exists in core (`Connection.status.exportedAPIs` from labeled
CRDs / the workspace boundary). The catalog adds **curation**: human-facing metadata
and sensible defaults that turn "a list of CRD names" into "a service you'd choose".

Group: `catalog.kbind.io`. Two kinds, successors of v1's
`APIServiceExportTemplate` and `Collection`:

```yaml
apiVersion: catalog.kbind.io/v1alpha1
kind: Export                          # one offering
metadata:
  name: mangodb
spec:
  title: MangoDB
  description: Managed MangoDB instances with automated backups.
  icon: { url: … }                    # optional
  docs: https://…                     # optional
  apis:                               # what a binding to this offering syncs
    - name: mangodbs.mangodb.io
    - name: mangodbbackups.mangodb.io
  defaults:                           # copied into the generated ClusterBinding
    conflictPolicy: Fail
    relatedResources:
      - group: ""
        resource: secrets
        direction: FromProvider
        selector:
          labelSelector:
            matchLabels:
              mangodb.io/managed: "true"
```

```yaml
apiVersion: catalog.kbind.io/v1alpha1
kind: Collection                      # grouping for UI/CLI browsing
metadata:
  name: databases
spec:
  title: Databases
  exports:
    - name: mangodb
```

Notes:

* The catalog is **derived-from-core-truth**: an `Export` listing an API that isn't
  actually exported (label/boundary) gets a condition; the gateway hides it. The label
  remains the source of truth, the catalog is presentation + defaults.
* These CRDs live on the provider and are read only by the gateway/UI/CLI. The
  konnector never sees them.

### 2. Issuer (provider-side controller)

Everything v1's `backend/kubernetes` did, made a named component. The issuer is a Go
interface (provision boundary, mint credentials, revoke); **the in-tree implementation
is plain Kubernetes only** — the kcp issuer lives in the separate `contrib/kcp`
distribution, which wires its own implementation against the same interface.

* Per consumer identity: provision the tenancy boundary — a namespace set on plain
  Kubernetes (workspaces, in the contrib/kcp issuer).
* Mint credentials: ServiceAccount + RBAC scoped to exactly the exported APIs (+
  declared related resources) within that boundary + kubeconfig. This fixes v1's
  cluster-admin-ish `kube-binder` ClusterRole (roadmap #303: reduced footprint). On a
  plain-Kubernetes provider "scoped to the exported APIs" is an explicit Role enumerating
  those resources; on the kcp/CRD-less flavor the tenancy boundary *is* the workspace, so
  scoping is the workspace grant itself (everything in it is exported by construction)
  rather than a per-resource enumeration — same `Issuer` interface, two scoping mechanisms
  matching the core's two schema sources.
* **Credential mechanism: long-lived SA token** (v1 behavior, secret-based
  ServiceAccount token). Trade-off accepted deliberately: zero rotation friction and no
  konnector-side refresh machinery, at the cost of security posture — and noting
  upstream Kubernetes is steering away from secret-based SA tokens, so this is
  revisitable without API change (the bundle's Secret is replaceable; a bounded-token +
  reissue mode can be added later behind the same interface). Revocation = delete the
  `Grant` → issuer deletes the SA/token.
* Records issuance in **`Grant`** (`iam.kbind.io` — an issuance/identity record, not
  catalog presentation): "identity X was issued credentials Y for export Z". The anchor
  for revocation, audit, and the reaper.

### 3. Gateway (HTTP API)

Stateless HTTP server (sessions externalized), serving:

One gateway fronts exactly **one provider** — catalog aggregation across providers is a
future layer above this one, already possible externally because the protocol's output
is just a bundle.

| Endpoint | Purpose |
|---|---|
| `GET /api/provider` | provider metadata + supported auth methods (successor of v1 `/api/exports`) |
| `GET /api/catalog` | `Export`s + `Collection`s visible to the caller |
| `POST /api/bind` | input: export name + consumer identity → drives issuer → returns a **one-time pickup URL** for the bundle |
| `GET /api/bundle/<token>` | single-use, short-TTL (5 min) bundle pickup — **returns the bundle**, then the token is dead |
| `POST /api/apply` | optional, flag-gated: browser-apply path — gateway applies the bundle (+ konnector install) into a consumer cluster using a caller-supplied consumer kubeconfig |
| `GET /api/authorize`, `/api/callback` | auth flow (delegated to authenticator plugin) |
| `GET /api/healthz` | health |

**The bundle is the one-apply file**, delivered through a one-time pickup URL so live
credentials are fetched exactly once and never stored at rest in the gateway. Content
negotiation on pickup: `application/yaml` returns the literal multi-doc bundle (Secret +
`Connection` + `ClusterBinding`); `application/json` wraps the same objects in a thin
envelope (`{ bundle: [...] }`). There is no other handshake state: no request objects to
poll, no phases to wait on. `curl` bind + pickup piped to `kubectl apply -f -` is a
complete client.

The single-use, short-TTL property applies to the **pickup URL**, not to the credential
inside it: the bundle's Secret carries the long-lived SA token minted by the issuer
(§2), so a bundle that has been picked up once stays valid and re-appliable. That is what
makes `-o yaml > binding.yaml` committed to git a real GitOps artifact — the pickup is
consumed once, the token it delivered keeps working until its `Grant` is revoked.
Re-running `bind` mints a fresh `Grant`/token and a new pickup; it does not invalidate a
previously committed bundle unless that `Grant` is explicitly revoked.

### 4. Auth (pluggable)

* `Authenticator` interface: `Routes()` (mounted under `/api/auth/…`) +
  `Authenticate(r) (Identity, error)`. Reference implementations: OIDC (code grant +
  PKCE, as v1) and `kubernetes` (TokenReview against the provider — for in-platform
  UIs that already hold a cluster identity).
* The embedded mock-OIDC server survives **only** as a dev-mode flag (`--oidc-mock`).
* OIDC is configured with kube-apiserver/kcp-style flags: `--oidc-issuer-url`,
  `--oidc-client-id`, `--oidc-client-secret`, `--oidc-ca-file`,
  `--oidc-username-claim`, `--oidc-groups-claim`, `--oidc-scopes`,
  `--oidc-redirect-url`.
* Sessions are **stateless encrypted cookies/tokens** (keys via
  `--cookie-signing-key`/`--cookie-encryption-key`) — no session store at all. With
  shared keys, any number of gateway replicas works out of the box; the same encrypted
  blob doubles as the CLI's bearer token. (Supersedes the earlier memory/Redis
  session-store idea: the only server-side state the flow ever needed — one-time bundle
  pickup — lives on the `Grant` via annotations with optimistic concurrency, so the
  gateway keeps zero state.)
* Identity → tenancy key: `issuer + "/" + subject` hash, as v1, so the same human gets
  the same boundary on re-bind.

### 5. Reaper (provider-side, optional)

The core leaves dead-consumer GC explicitly to this layer, keyed off the per-Connection
`Lease` the konnector maintains. That core primitive is implemented (the konnector
renews a `coordination.k8s.io/Lease` per Connection on the provider), so the reaper is
unblocked.

* Lease expired beyond TTL → mark the issuance stale → (configurably) revoke
  credentials, then delete kube-bind-created namespaces and synced objects.
* TTLs and the destructive step are opt-in and conservative by default (revoke ≠
  delete; deletion requires explicit enablement).

### 6. CLI (`kbind`)

Thin client over the gateway; everything it does is reproducible by hand. Named `kbind`
(a copy/symlink as `kubectl-bind` makes it a kubectl plugin). The **krew plugin name
stays `bind`** (kept from v1 — `kubectl krew install bind` keeps working): release
archives ship the binary as `kubectl-bind`, and krew-release-bot renders `.krew.yaml`
against each GitHub release to PR krew-index (`.github/workflows/cli.yaml`).

```sh
kubectl bind login https://mangodb.example.com               # auth, cache token
kubectl bind catalog                                         # list Exports/Collections
kubectl bind export mangodb                             # bind an Export:
                                                      #   POST /api/bind → bundle
                                                      #   → apply (or -o yaml)
kubectl bind export mangodb -o yaml > binding.yaml      # GitOps mode: print, don't apply
```

* `--install-konnector` (default on for interactive use) installs/upgrades the v2
  konnector, as v1 did.
* The CLI never creates bespoke objects — it applies the gateway's bundle verbatim.
  `-o yaml` output committed to git is byte-for-byte the GitOps path.
* v1 subcommands that existed to ferry the old handshake (`apiservice`, `deploy`,
  per-resource polling) disappear.

### 7. UI (SPA)

Browse catalog → authenticate → bind → then either:

* **download the bundle** (via the one-time pickup URL) / copy a `kubectl bind`
  one-liner, or
* **browser-apply** (v1's UI-only flow, roadmap #406, kept): the user supplies a
  consumer-cluster kubeconfig (or the UI runs in-platform where one is already held),
  and the gateway's `/api/apply` applies the bundle and installs the konnector into the
  consumer cluster.

The UI is a pure gateway client; it holds no flow state the gateway doesn't have.
Browser-apply is flag-gated on the gateway and off by default — it means consumer
credentials transit the gateway, which deployments must consciously accept.

## Packaging & repo

Post-restructure layout (the repo root is the kbind module; `sdk/` is the standalone
type module):

```
kbind/
├── sdk/apis/core/v1alpha1/       # core (implemented)
├── sdk/apis/catalog/v1alpha1/    # Export, Collection
├── sdk/apis/iam/v1alpha1/        # Grant
├── backend/                      # this proposal's server-side packages
│   ├── auth/                     #   Authenticator iface, OIDC (+dev mock), sessions
│   ├── issuer/                   #   Issuer iface + kube impl + Grant controller
│   ├── gateway/                  #   HTTP API + embedded UI
│   └── reaper/                   #   Lease-keyed GC
├── cli/                          # kbind CLI packages
├── web/                          # SPA sources (embedded into the gateway)
├── cmd/konnector/                # core (implemented)
├── cmd/backend/                  # kbind-backend binary
└── cmd/kbind/                    # CLI binary
```

The backend ships as **one binary with module flags** (`--enable-gateway`,
`--enable-issuer`, `--enable-reaper`, `--enable-apply`) — operational simplicity over
purity; the boundaries stay as Go packages so a future split costs a `main.go`, not a
refactor. Types the backend serves live in `sdk` (separate module) so integrations can
depend on the APIs without the server. A future kcp distribution provides its own issuer
implementation behind the same interface (it no longer lives in this repo).

## Migration notes

* The v1 wire protocol is not bridged: v1 CLI cannot talk to a v2 gateway. Both stacks
  can run side by side on one provider during transition (different endpoints, disjoint
  CRD groups).
* v1 catalog objects (`APIServiceExportTemplate`/`Collection`) translate mechanically
  to `Export`/`Collection`; a converter script ships with the backend.

## Decided

* **Packaging**: one `kube-bind-backend` binary; gateway/issuer/reaper/apply are module
  flags, boundaries kept as Go packages.
* **Issuance anchor**: `Grant` in `iam.kbind.io` — the typed record of
  "identity X was issued credentials Y for export Z"; anchor for revocation, audit,
  reaper. Kept out of `catalog.kbind.io` so that group stays purely presentation+defaults.
* **Credentials**: long-lived secret-based SA token (v1 behavior) — zero rotation
  friction accepted over security posture; revocation via `Grant` deletion; bounded
  tokens addable later behind the same issuer interface without API change.
* **Catalog vocabulary**: `Export` + `Collection`.
* **Bundle delivery**: one-time pickup URL, 5-minute TTL, single use — the TTL/single-use
  applies to the *pickup URL*, not the long-lived SA token inside, so a picked-up bundle
  stays re-appliable (GitOps-safe). The bundle is never stored at rest in the gateway.
* **kcp**: stays a separate distribution (`contrib/kcp`) providing its own issuer
  implementation; the in-tree backend issuer is plain Kubernetes only.
* **UI reach**: browser-apply path **kept** (roadmap #406) — gateway `/api/apply`
  applies bundle + installs konnector into the consumer cluster with a caller-supplied
  kubeconfig; flag-gated, off by default, consumer credentials transiting the gateway
  is an explicitly accepted trade-off when enabled.
* **Federation**: one gateway = one provider; cross-provider aggregation is a future
  layer above the bundle protocol.

Added 2026-07-29 (implementation round):

* **Naming**: project is **kbind**. Groups `catalog.kbind.io` (Export, Collection) and
  `iam.kbind.io` (Grant); binaries `kbind-backend` and `kbind` (CLI, kubectl-plugin
  compatible); packages `backend/`, `cli/`, `web/` in the root module.
* **Sessions**: stateless encrypted cookie/bearer tokens, no session store (see §4).
  HA needs only shared cookie keys across replicas.
* **One-time pickup without gateway state**: the pickup token is
  `<grant-name>.<random>`; the gateway stamps `sha256(random)` + expiry as annotations
  on the `Grant` at bind time and removes them (optimistic concurrency) on pickup. The
  bundle itself is (re)constructed on demand from the issuer's token Secret — never
  stored at rest, single-use enforced by the API server, HA-safe.
* **Grant is spec-resolved at bind time**: the gateway copies the `Export`'s API list +
  defaults into `Grant.spec`, so issuance is a stable record even if the catalog entry
  changes later. The issuer controller provisions namespace/SA/RBAC from `Grant.spec`
  and reports the artifacts in `Grant.status`; deletion (revocation) unwinds via an
  `iam.kbind.io/cleanup` finalizer.
* **Lease ↔ Grant link (per-grant)**: the issued kubeconfig pins its context namespace
  to the per-consumer boundary namespace, and the konnector's heartbeat writes its
  Lease into the kubeconfig's context namespace when set (falling back to the `kbind`
  namespace). The Lease is named `<connection>-<consumer-uid-prefix>` and annotated
  with its Connection name; since the bundle names the Connection after the Grant, the
  reaper judges staleness **per grant** by that annotation. Fallback for hand-renamed
  Connections is conservative: any fresh Lease in the boundary keeps its Grants alive,
  only a fully silent boundary goes stale. RBAC for Leases stays scoped to the
  tenant's own namespace.
* **kubernetes authenticator**: implemented as **TokenReview** (not SAR — SAR is
  authorization; identity comes from TokenReview) against the provider, behind
  `--kubernetes-auth`. Tenancy key `kubernetes#<username>`. No interactive routes —
  in-platform callers just send their own bearer token.
* **Packaging (charts)**: `deploy/charts/backend` ships the service layer (module
  flags as values, catalog+iam CRDs, Service, RBAC incl. `escalate`/`bind` so the
  issuer may create Roles enumerating APIs the backend itself does not hold; OIDC
  client secret and cookie keys via existing Secrets).
* **UI**: dependency-free static SPA (embedded via `go:embed` into the gateway) — no
  build toolchain in the repo; pure gateway client.

## Open questions

None — initial design questions resolved (see **Decided**). New questions raised during
review go here.
