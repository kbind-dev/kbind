# kube-bind v2 Extended: Backend API, CLI, UI

* Status: **DRAFT — for iteration**
* Authors: @mjudeikis
* Date: 2026-06-10
* Builds on: [v2-slim-core.md](v2-slim-core.md) (Proposed))

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

Group: `catalog.kube-bind.io`. Two kinds, successors of v1's
`APIServiceExportTemplate` and `Collection`:

```yaml
apiVersion: catalog.kube-bind.io/v1alpha1
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
apiVersion: catalog.kube-bind.io/v1alpha1
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
  cluster-admin-ish `kube-binder` ClusterRole (roadmap #303: reduced footprint).
* **Credential mechanism: long-lived SA token** (v1 behavior, secret-based
  ServiceAccount token). Trade-off accepted deliberately: zero rotation friction and no
  konnector-side refresh machinery, at the cost of security posture — and noting
  upstream Kubernetes is steering away from secret-based SA tokens, so this is
  revisitable without API change (the bundle's Secret is replaceable; a bounded-token +
  reissue mode can be added later behind the same interface). Revocation = delete the
  `Grant` → issuer deletes the SA/token.
* Records issuance in **`Grant`** (`catalog.kube-bind.io`): "identity X was issued
  credentials Y for export Z". The anchor for revocation, audit, and the reaper.

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

### 4. Auth (pluggable)

* `Authenticator` interface: `Routes()` (mounted under `/api/auth/…`) +
  `Authenticate(r) (Identity, error)`. Reference implementations: OIDC (code grant +
  PKCE, as v1) and `kubernetes` (SubjectAccessReview against the provider — for
  in-platform UIs that already hold a cluster identity).
* The embedded mock-OIDC server survives **only** as a dev-mode flag.
* Session store stays an interface (memory, Redis as today) but the gateway must be
  fully functional with ≥2 replicas out of the box — Redis (or any external store) is
  the documented production default, memory is dev-only.
* Identity → tenancy key: `issuer + "/" + subject` hash, as v1, so the same human gets
  the same boundary on re-bind.

### 5. Reaper (provider-side, optional)

The core leaves dead-consumer GC explicitly to this layer, keyed off the per-Connection
`Lease` the konnector maintains:

* Lease expired beyond TTL → mark the issuance stale → (configurably) revoke
  credentials, then delete kube-bind-created namespaces and synced objects.
* TTLs and the destructive step are opt-in and conservative by default (revoke ≠
  delete; deletion requires explicit enablement).

### 6. CLI (`kubectl bind`)

Thin client over the gateway; everything it does is reproducible by hand:

```sh
kubectl bind login https://mangodb.example.com        # auth, cache token
kubectl bind catalog                                  # list Exports/Collections
kubectl bind mangodb                                  # bind an Export:
                                                      #   POST /api/bind → bundle
                                                      #   → kubectl apply (or -o yaml)
kubectl bind mangodb -o yaml > binding.yaml           # GitOps mode: print, don't apply
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

Per the frozen core layout: `v2/backend` (gateway + issuer + reaper + auth), `v2/cli`,
`v2/web`. The backend ships as **one binary with module flags** (`--enable-gateway`,
`--enable-issuer`, `--enable-reaper`, `--enable-apply`) — operational simplicity over
purity; the boundaries stay as Go packages so a future split costs a `main.go`, not a
refactor. All of it depends on `v2/sdk` only. The kcp distribution (`contrib/kcp`)
remains separate, providing its own issuer implementation behind the same interface.

## Migration notes

* The v1 wire protocol is not bridged: v1 CLI cannot talk to a v2 gateway. Both stacks
  can run side by side on one provider during transition (different endpoints, disjoint
  CRD groups).
* v1 catalog objects (`APIServiceExportTemplate`/`Collection`) translate mechanically
  to `Export`/`Collection`; a converter script ships with the backend.

## Decided

* **Packaging**: one `kube-bind-backend` binary; gateway/issuer/reaper/apply are module
  flags, boundaries kept as Go packages.
* **Issuance anchor**: `Grant` in `catalog.kube-bind.io` — the typed record of
  "identity X was issued credentials Y for export Z"; anchor for revocation, audit,
  reaper.
* **Credentials**: long-lived secret-based SA token (v1 behavior) — zero rotation
  friction accepted over security posture; revocation via `Grant` deletion; bounded
  tokens addable later behind the same issuer interface without API change.
* **Catalog vocabulary**: `Export` + `Collection`.
* **Bundle delivery**: one-time pickup URL, 5-minute TTL, single use; bundle never
  stored at rest in the gateway.
* **kcp**: stays a separate distribution (`contrib/kcp`) providing its own issuer
  implementation; the in-tree backend issuer is plain Kubernetes only.
* **UI reach**: browser-apply path **kept** (roadmap #406) — gateway `/api/apply`
  applies bundle + installs konnector into the consumer cluster with a caller-supplied
  kubeconfig; flag-gated, off by default, consumer credentials transiting the gateway
  is an explicitly accepted trade-off when enabled.
* **Federation**: one gateway = one provider; cross-provider aggregation is a future
  layer above the bundle protocol.

## Open questions

None — initial design questions resolved (see **Decided**). New questions raised during
review go here.
