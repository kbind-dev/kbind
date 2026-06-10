# kube-bind v2: Slim Core, Pluggable Everything Else

* Status: **Proposed**
* Authors: @mjudeikis
* Date: 2026-06-10
* Supersedes parts of: [backend-only-bindings.md](backend-only-bindings.md)
* Follow-up: [v2-extended.md](v2-extended.md) — the optional service layer (backend API,
  CLI, UI) built on this contract

Changes to this document now require re-opening the decision log in **Decided**; the
remaining **Open questions** (OpenAPI fidelity spike, built-ins filter) are tracked for
implementation, not re-design.

## Summary

kube-bind v2 splits the project into a **slim sync core** and an **optional service
layer**. The core does exactly one thing: given a Secret containing a kubeconfig, a
connection object, and a binding per API, it syncs CRDs and resource instances between a
consumer and a provider cluster — same scope, same names, no transformation — and reports
conflicts instead of papering over them.

Everything else — OIDC, the auth handshake, the web UI, the CLI, templates, collections,
service-account provisioning — moves out of the core into optional, pluggable components
whose only job is to *produce the core inputs* (the kubeconfig Secret, the `Connection`,
and the `Binding`/`ClusterBinding` objects).

```
                        v2 contract

  ┌────────────────────────────────────────────────────────┐
  │  optional layer (any of: backend+OIDC+UI, CLI, Helm,   │
  │  GitOps, kcp integration, your own controller…)        │
  └───────────────┬────────────────────────────────────────┘
                  │ produces
                  ▼
   Secret (kubeconfig) + Connection (provider link + schema pull)
                       + Binding / ClusterBinding (per bound API)
                  │ consumed by
                  ▼
  ┌────────────────────────────────────────────────────────┐
  │  core: konnector — CRD sync, spec ⇩up / status ⇩down,  │
  │  related resources, conflict detection                 │
  └────────────────────────────────────────────────────────┘
```

## Motivation

### What v1 got right

* The split of *spec flows consumer → provider, status flows provider → consumer* is sound
  and battle-tested ([spec_reconcile.go](../../pkg/konnector/controllers/cluster/serviceexport/spec/spec_reconcile.go),
  [status_reconcile.go](../../pkg/konnector/controllers/cluster/serviceexport/status/status_reconcile.go)).
* The finalizer pattern (`kubebind.io/syncer` blocks consumer deletion until the provider
  copy is gone) prevents data loss.
* `APIServiceBinding` already approximates the minimal object: it is essentially
  `kubeconfigSecretRef` + conditions.
* The backend-only-bindings proposal proved there is real demand for a flow with no HTTP,
  no OIDC, no browser.

### What makes v1 heavy

1. **The isolation/scope machinery.** Three isolation modes (`Prefixed`, `Namespaced`,
   `None`) × two informer scopes (`Cluster`, `Namespaced`) × `APIServiceNamespace`
   namespace-mapping objects. Cluster-scoped resources can be *converted* to namespaced on
   the provider, names get rewritten, namespaces get re-mapped. This is the single largest
   source of complexity in the syncer (the whole
   [isolation/](../../pkg/konnector/controllers/cluster/serviceexport/isolation/) strategy
   layer plus `APIServiceNamespace` round-trips in every reconcile), and the source of the
   most confusing API fields (`informerScope` vs `isolation` vs deprecated
   `clusterScopedIsolation` vs CRD `scope`).

2. **The object zoo.** A bind today touches up to ten CRDs: `APIServiceExportTemplate`,
   `Collection`, `APIServiceExportRequest`, `APIServiceExport`, `BoundSchema`,
   `APIServiceNamespace`, `ClusterBinding`, `APIServiceBinding`,
   `APIServiceBindingBundle`, `BindableResourcesRequest`. Several exist only to ferry the
   *handshake* (template discovery, request/response), not to drive sync.

3. **Coupled service layer.** The backend bundles OIDC (even an embedded mock OIDC
   server), session storage, cookie handling, an SPA, kubeconfig minting, and the
   provider-side controllers in one binary. `--frontend-disabled` exists but is a flag on
   a monolith, not an architecture.

4. **Sync engine inefficiencies.** Per-export dynamic controller spawning duplicates
   informers per GVR; the konnector watches *all* secrets cluster-wide; heartbeat interval
   and many behaviors are hardcoded.

## Goals

* **One apply.** A full end-to-end binding to a provider is a single
  `kubectl apply -f` of one file containing all objects (Secret + `Connection` +
  bindings). Nothing else, nothing more: no handshake, no waiting on phases, no ordering
  requirements — the konnector converges from whatever has been applied.
* Scope-preserving, name-preserving sync ("plain sync"): namespaced ↔ namespaced in the
  same namespace name, cluster-scoped ↔ cluster-scoped under the same name.
* First-class conflict handling: never silently fight over or adopt objects another actor
  owns.
* CRD/schema delivery in core: applying the core objects yields working APIs on the
  consumer.
* Related-resource sync (Secrets/ConfigMaps referenced by synced objects) in core.
* Zero kube-bind components required on the provider cluster for the core path.
* Portability and extension points so v1-style behaviors (tenancy mapping, handshakes,
  UIs) can be rebuilt *on top*.

## Non-Goals

* In-core multi-consumer isolation on a shared provider namespace-space. Tenancy becomes a
  deployment concern (dedicated namespaces/cluster/kcp workspace per consumer) or an
  extension (see [Mapper](#extension-point-1-mapper)).
* Scope conversion (cluster-scoped → namespaced) of any kind.
* In-core auth: OIDC, sessions, browser flows, token minting.
* Automatic conversion from v1alpha2 objects (see [Migration](#migration)).

## Design

### The core API: three kinds + a Secret

**0. Provider opt-in is a label or a boundary, not a CRD.** On a plain Kubernetes
provider, the operator marks which CRDs are exported by labeling them:

```sh
kubectl label crd mangodbs.mangodb.io "core.kube-bind.io/exported=true"
```

Whatever carries the label *and* is readable by the consumer's credentials is exported.

On **CRD-less providers** (kcp and kcp-like systems — APIs served from APIBindings /
logical clusters, no CRD objects to label), the export boundary is the **logical cluster
itself**: the kubeconfig points at a workspace that was deliberately populated with
exactly the APIs to offer, so everything discoverable there (minus built-ins) is
exported. No label needed — curation happened when the workspace was assembled.

Either way this is the entire provider-side footprint of the core: a label (or a
workspace boundary) plus RBAC. The credentials' RBAC *is* the authorization model; there
is no separate permission/claim grant object in core.

**1. A Secret with a kubeconfig** pointing at the provider cluster, living in the
konnector's designated namespace (default `kube-bind`).

**2. `Connection`** (cluster-scoped) — the link to one provider. Owns credentials and
schema delivery, and surfaces what the provider offers:

```yaml
apiVersion: core.kube-bind.io/v1alpha1
kind: Connection
metadata:
  name: mangodb-provider
spec:
  # Immutable. The only credential reference in the core.
  kubeconfigSecretRef:
    namespace: kube-bind
    name: mangodb-provider
    key: kubeconfig

  # How exported APIs reach the consumer.
  schema:
    source: Auto           # Auto (default): CRD if readable on the provider, else OpenAPI
                           # CRD:     read apiextensions CRDs verbatim
                           # OpenAPI: synthesize CRDs from discovery + /openapi/v3
                           #          (CRD-less providers: kcp, kcp-like systems)
    pullPolicy: Bound      # Bound (default): pull only APIs that have a Binding/ClusterBinding
                           # All:     eagerly pull every exported API readable by the credentials
                           # None:    never install CRDs (user/extension manages them)
    updatePolicy: Always   # Always: follow provider schema changes | Once: pin at first pull

  # Bind everything: konnector maintains a managed ClusterBinding (named after this
  # Connection) covering all exported APIs, kept in sync with status.exportedAPIs.
  autoBind: false
status:
  exportedAPIs:             # discovery result: exported CRDs visible to these credentials
    - name: mangodbs.mangodb.io
      scope: Namespaced
      versions: ["v1"]
  conditions: []           # SecretValid, Connected, SchemaInSync
```

**3. `ClusterBinding`** (cluster-scoped) and **`Binding`** (namespaced) — activate
instance sync for one or more APIs, following the `ClusterRole`/`Role` convention. APIs
are referenced by their **CRD name** on the provider (`<plural>.<group>`) — no
group/version/resource triples to get out of sync with the schema:

```yaml
apiVersion: core.kube-bind.io/v1alpha1
kind: ClusterBinding              # syncs objects of these APIs cluster-wide
metadata:
  name: mangodb
spec:
  connectionRef:
    name: mangodb-provider

  # One or more exported CRDs to sync, by CRD name on the provider.
  apis:
    - name: mangodbs.mangodb.io
    - name: mangodbbackups.mangodb.io

  # Conflict behavior for pre-existing target objects (both directions).
  conflictPolicy: Fail            # Fail | Adopt

  # Related resources synced alongside instances of the bound APIs.
  # Trimmed v1 PermissionClaims: explicit, reviewable, no JSONPath references in core v1.
  relatedResources:
    - group: ""
      resource: secrets
      direction: FromProvider     # FromProvider | FromConsumer
      selector:
        labelSelector:
          matchLabels:
            mangodb.io/managed: "true"
status:
  conditions: []                  # Connected, Synced, Conflicts, PermissionDenied
  boundAPIs:                      # per-API observed state
    - name: mangodbs.mangodb.io
      crdHash: sha256:…           # applied schema version
      conflictCount: 0            # objects skipped due to foreign ownership;
                                  # per-object detail lives on each object's own condition
```

```yaml
apiVersion: core.kube-bind.io/v1alpha1
kind: Binding                     # same spec, but syncs only objects in its own namespace
metadata:
  name: mangodb
  namespace: team-a
spec:
  connectionRef:
    name: mangodb-provider
  apis:
    - name: mangodbs.mangodb.io
```

The namespaced `Binding` gives teams self-service sync scoped to their namespace with
plain namespace RBAC, and is the v2 answer to v1's `informerScope: Namespaced`. A
`Binding` listing a cluster-scoped CRD is invalid for that entry (per-API condition, no
sync). Where a `ClusterBinding` and a `Binding` cover the same API on the same
connection, the `ClusterBinding` wins and the `Binding` gets a per-API condition.
"Bind everything" is `Connection.spec.autoBind: true` — the konnector then maintains a
managed `ClusterBinding` mirroring `status.exportedAPIs`.

That is the **entire core API**: three kinds plus the Secret. No kube-bind CRDs are
required on the provider; the konnector reads provider CRDs through the ordinary
apiextensions API and reads/writes instances through the dynamic client.

### The whole UX: one apply

The acceptance test for the core is that a complete binding is **one file, one command**
(konnector already installed, once, like any controller):

```yaml
# mangodb-binding.yaml
apiVersion: v1
kind: Secret
metadata:
  name: mangodb-provider
  namespace: kube-bind
stringData:
  kubeconfig: |
    <provider kubeconfig>
---
apiVersion: core.kube-bind.io/v1alpha1
kind: Connection
metadata:
  name: mangodb-provider
spec:
  kubeconfigSecretRef:
    namespace: kube-bind
    name: mangodb-provider
    key: kubeconfig
---
apiVersion: core.kube-bind.io/v1alpha1
kind: ClusterBinding
metadata:
  name: mangodb
spec:
  connectionRef:
    name: mangodb-provider
  apis:
    - name: mangodbs.mangodb.io
```

```sh
kubectl apply -f mangodb-binding.yaml
```

…and the APIs are live on the consumer. Design rule this imposes: **all core objects are
order-independent and level-triggered**. A binding referencing a not-yet-existing
`Connection`, a `Connection` referencing a not-yet-existing Secret, a binding listing a
not-yet-exported CRD — none of these are errors, all are `Pending` conditions that
resolve when the missing piece arrives. No phase gating, no request/response objects, no
controller that must answer before the user may apply the next thing.

Unbind has one ordering constraint that apply does not. `kubectl delete -f
mangodb-binding.yaml` deletes the `Connection`, bindings, and synced objects in whatever
order the server processes them, but every synced consumer object carries a finalizer
whose release requires the konnector to reach the provider through the *very* `Connection`
being deleted. The konnector therefore drains object finalizers before a referenced
`Connection`/binding is allowed to finalize: one that still has synced objects bound to it
stays in `Terminating` with a `DrainingObjects` condition until those objects' provider
copies are gone, then releases. "Delete the whole file" converges — it does not deadlock —
but the `Connection` is the *last* object to disappear, not the first.

### Schema sources: CRD and OpenAPI

The konnector must install a working CRD on the consumer for every bound API. Two ways
to obtain it, selected by `Connection.spec.schema.source` (`Auto` probes CRD first,
falls back to OpenAPI):

* **`CRD`** — read the CRD from the provider's apiextensions API and apply it on the
  consumer with mechanical adjustments: never copy provider-side `conversion.strategy:
  Webhook` or its caBundle (such CRDs are refused — per-API condition + skip, see the
  sync table), add an owner-ref to the `Connection`, and inject UX-only printer
  columns/categories. To keep a multi-version CRD installable on the consumer *without* a
  conversion webhook, install only the provider's **storage/served version** rather than
  every historical version. Highest fidelity; requires the provider to *have* CRDs and
  the credentials to read them.
* **`OpenAPI`** — for CRD-less providers (kcp and kcp-like systems, aggregated APIs):
  synthesize a CRD from what every Kubernetes-shaped API server already serves:
  * **discovery** (`/apis/<group>/<version>`) → plural/singular/kind/shortNames,
    categories, `namespaced`, verbs, and the served versions of the group;
  * **`/openapi/v3/apis/<group>/<version>`** → the structural schema per version,
    including `x-kubernetes-*` extensions and defaults;
  * subresource detection (status/scale) from the OpenAPI paths.

  Known fidelity limits, stated honestly: CEL validation rules and other
  server-side-only constraints may not round-trip through OpenAPI — the consumer-side
  CRD can be *looser* than the provider's real validation, and the provider remains the
  enforcing side (a consumer object that passes locally can still be rejected upstream;
  that surfaces as a per-object sync condition, not silent loss).

Binding by CRD name (`<plural>.<group>`) works identically under both sources — it's
just resource + group; with the OpenAPI source there simply is no CRD object behind the
name on the provider. `status.exportedAPIs` is computed per source: labeled CRDs for
`CRD`, discovery (minus built-in groups) for `OpenAPI`.

### Sync semantics

| Aspect | v2 behavior |
|---|---|
| Identity | `ns/name` on consumer == `ns/name` on provider. Cluster-scoped stays cluster-scoped. No prefixes, no remapping, no `APIServiceNamespace`. |
| Selection | `ClusterBinding` syncs all instances of its listed APIs cluster-wide; namespaced `Binding` syncs only instances in its own namespace. `Connection.spec.autoBind` binds everything exported. |
| Spec | consumer → provider, server-side apply with a dedicated field manager. |
| Status | provider → consumer, status subresource update. |
| Namespaces | If the consumer namespace doesn't exist on the provider, the konnector creates it (annotated as kube-bind-created; only kube-bind-created namespaces are cleaned up on unbind). |
| Deletion | Finalizer on consumer object (as today); consumer deletion propagates to provider, finalizer released only when the provider copy is fully gone (a provider-side finalizer holds the consumer object in `Terminating` until it clears). `kube-bind.io/deletion-policy: Orphan` on a consumer object releases the finalizer without deleting the provider copy. Provider-side deletion of a synced object is treated as drift and re-created **unless** the provider copy has a non-zero deletionTimestamp — then the konnector waits for it to finalize rather than racing a re-create. Consumer is source of truth for spec. |
| Schema | Per `Connection.spec.schema`: konnector obtains schemas via `source: CRD` (read apiextensions) or `OpenAPI` (synthesize from discovery + `/openapi/v3` — CRD-less providers like kcp), pulls per `pullPolicy: All` or `Bound`, applies on the consumer (owner-ref to the `Connection`). `updatePolicy: Always` keeps following provider schema changes; `Once` pins. CRDs with `strategy: Webhook` are refused (per-API condition + skip). |
| Discovery | `Connection.status.exportedAPIs`: labeled CRDs (`source: CRD`) or discovery minus built-ins (`source: OpenAPI`, where the logical-cluster boundary is the export boundary) — core-level discovery with zero provider CRDs. |
| Related resources | Selected Secrets/ConfigMaps sync in the declared direction, same identity rules, scoped like their binding. They are owned by the **binding** (not by individual instances): an object is synced while it matches the selector and is garbage-collected when it stops matching or the binding is removed. The same ownership markers and `conflictPolicy` apply — a related object already owned by another binding/consumer is a conflict, never silently overwritten. |
| RBAC | The supplied credentials' RBAC *is* the authorization model; partial RBAC is an expected steady state, not a failure. Each forbidden operation surfaces as a typed `PermissionDenied` condition naming the verb+resource refused (per-API on the binding, per-object on the instance); the konnector keeps syncing everything it *is* allowed to and never fails a whole binding because one resource or namespace is out of reach. |
| Informers | One shared dynamic informer per GVR per connection. Cluster-wide informers for `ClusterBinding`; namespace-scoped informers where only namespaced `Binding`s exist. |

### Conflict handling (the new hard part)

Identity mapping means two writers can legitimately collide. Core rules:

1. Every object the konnector writes carries ownership markers: the **binding UID** plus
   the **source cluster UID**. The source cluster UID is the identity the `Connection`
   pins in its status the first time it resolves its credentials — *not* something read
   from a fixed object like the `kube-system` namespace, which does not exist on
   CRD-less/logical-cluster providers (kcp). This keeps the marker well-defined under both
   schema sources. (Same idea as today's `kube-bind.io/consumer-uid` / `provider-uid`
   annotations, kept.)
2. Before first write, the konnector classifies the existing target object three ways:
   * **No markers** (a foreign, un-owned object): `conflictPolicy: Fail` (default) does
     not touch it; `conflictPolicy: Adopt` stamps markers and SSA force-applies.
   * **Our markers, our current binding + object UID**: ours — normal SSA update.
   * **Our cluster's markers but a stale/foreign binding-or-object UID**, *or* **another
     cluster's markers**: always a conflict, regardless of `conflictPolicy`. `Adopt`
     never steals an object another binding/consumer owns; first-writer-wins,
     deterministically.
3. Conflicts are recorded **on the conflicting object itself** as a typed condition that
   distinguishes the two cases an operator remediates differently — `ForeignObjectExists`
   (no markers; rename or switch to `Adopt`) vs. `OwnedByAnother` (claimed by a different
   binding/consumer; pick another name). The binding does **not** inline an unbounded list
   of object names: its status carries only a `Conflicts` condition and a count
   (`boundAPIs[].conflictCount`), plus an Event. The konnector keeps syncing everything
   else.
4. Field-level conflicts within an owned object are resolved by SSA with
   `force=true` for our field manager on our sync direction only (spec fields upstream,
   status fields downstream) — same as today, but now stated as the contract.

### Topology: the konnector is the only running component

Consumer-side pull, as today — and **the core has no backend**. The konnector is the
single process in the entire core path; everything v1's backend did for sync either
moved into the konnector or out of core:

| v1 backend job | v2 home |
|---|---|
| serve template/catalog discovery | konnector reads exported-CRD labels → `Connection.status.exportedAPIs` |
| materialize exports/schemas (`BoundSchema`) | konnector reads provider CRDs via apiextensions API directly |
| create provider namespaces (`APIServiceNamespace` controller) | konnector creates them (iff RBAC allows) |
| track consumer liveness (`ClusterBinding` heartbeat) | konnector maintains a `Lease` on the provider |
| mint kubeconfigs / service accounts, rotate credentials | **out of core** — service layer or manual |
| GC artifacts of dead/vanished consumers | **out of core** — service layer can reap based on expired Leases |

* The konnector runs in the consumer cluster (or out-of-cluster with a consumer
  kubeconfig — it must not assume in-cluster). One konnector may serve many consumer
  logical clusters (kcp workspaces, a cluster fleet) — see
  [Engine](#engine-built-on-multicluster-runtime).
* It watches `Connection`, `ClusterBinding`, and `Binding` objects, and only the Secrets
  that `Connection`s reference (fixes the v1 watch-all-secrets issue). A `Connection`'s
  `kubeconfigSecretRef` must resolve to the konnector's own designated namespace (default
  `kube-bind`): `Connection` is cluster-scoped, so letting it name a Secret in any
  namespace would let anyone who can create a `Connection` read any Secret the konnector's
  ServiceAccount can. Cross-namespace refs are rejected with `SecretValid=False`.
* One `Connection` = one provider client/informer context; all bindings referencing it
  share that context.
* The provider cluster needs: reachable API server + RBAC for the supplied credentials.
  No backend, no controllers, no CRDs.

The honest cost of zero provider-side runtime: credential issuance/rotation and cleanup
after consumers that disappear forever are nobody's job in core. The Lease per
`Connection` is the hook — an optional provider-side reaper (service layer) can GC
kube-bind-created namespaces and synced objects whose Lease has expired.

Heartbeat keeps the zero-CRD property: the konnector maintains a plain
`coordination.k8s.io/Lease` per `Connection` in a designated provider namespace.
Anything richer (v1 `ClusterBinding`-style status) is an optional provider-side
component in the service layer.

### Engine: built on multicluster-runtime

The v2 konnector is built on
[multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime)
(roadmap #299), not hand-rolled informer plumbing — and structured as a **bridge between
two cluster sets**, each fronted by an mcr provider:

* **Provider side**: a custom mcr provider discovers "clusters" from `Connection`
  objects: one `Connection` = one logical cluster (engaged when the Connection's secret
  resolves, disengaged on deletion).
* **Consumer side**: also behind an mcr provider abstraction. The default is the trivial
  single-cluster provider (today's shape: one konnector in one consumer cluster). But
  nothing in the engine assumes one consumer cluster — swapping the consumer-side
  provider scales the same binary out:
  * **kcp provider** → one konnector per kcp instance, serving *every* workspace; each
    workspace carries its own `Connection`/`Binding` objects and is its own sync domain.
  * **fleet provider** (kubeconfig/cluster-inventory) → one konnector serving many
    physical consumer clusters, connecting a *set* of consumer clusters to a *set* of
    providers.
* The sync domain is always the pair *(consumer logical cluster, `Connection` within
  it)* — the core API is unchanged regardless of how many consumer clusters one
  konnector serves. Multi-consumer is purely an engine/deployment dimension, never an
  API dimension.
* Sync controllers are written once as multi-cluster-aware reconcilers; per-connection
  client/cache lifecycle, informer dedup per GVR, and teardown come from the framework
  instead of v1's contextstore + dynamic controller spawning.
* The backend already uses multicluster-runtime (`mcmanager` + the kcp apiexport
  provider) — v2 aligns consumer and provider sides on one runtime model, and the kcp
  flavor falls out of swapping the mcr provider rather than special-casing the engine.

Alpha ships the single-cluster consumer provider only; the kcp and fleet consumer
providers are the explicit scale-out path, unlocked by the architecture rather than
designed later.

### Extension point 1: Mapper

Core ships **identity mapping only**, but the place where v1's isolation logic lived
becomes a narrow Go interface so out-of-tree builds can restore tenancy mapping without
forking the engine:

```go
// Mapper translates object identity between consumer and provider.
// Core registers exactly one implementation: Identity.
type Mapper interface {
    // ToProvider maps a consumer object key to its provider key.
    ToProvider(gvr schema.GroupVersionResource, key ObjectKey) (ObjectKey, error)
    // ToConsumer is the inverse; must round-trip.
    ToConsumer(gvr schema.GroupVersionResource, key ObjectKey) (ObjectKey, error)
}
```

Notes:

* This is a *compile-time* extension (custom konnector build), not a CRD-configurable
  one. Keeping it out of the API keeps the API honest: the core CRD never promises
  renaming.
* The v1 `Prefixed` behavior is implementable as a Mapper; `Namespaced` (scope
  conversion) is intentionally **not** implementable — the interface maps keys, it cannot
  change scope. That's the hard line v2 draws.
* Possible second interface later: `Transformer` (mutate object payload before write —
  label injection, field stripping). Deliberately deferred.

### Extension point 2: the handshake

Everything that today negotiates *which* resources to bind and *how to get credentials*
becomes "anything that can create a Secret, a `Connection`, and bindings":

| v1 component | v2 home |
|---|---|
| backend HTTP API, OIDC, sessions, SPA/UI | optional `kube-bind-backend` component (own module/repo dir, own release cycle). Its output is exactly the core objects. |
| `kubectl bind` CLI + browser dance | optional `cli/` plugin, talks to the backend, ends by applying the core objects + (optionally) installing the konnector. |
| `APIServiceExportTemplate`, `Collection` | backend-layer CRDs (rich catalog: descriptions, grouping, permission review). Raw discovery is core (`Connection.status.exportedAPIs`); curation is service layer. |
| `APIServiceExportRequest`, `BindableResourcesRequest`, `BindingResourceResponse` | backend-layer wire/handshake types. |
| `APIServiceBindingBundle` ("bind everything") | absorbed into core as `Connection.spec.autoBind: true` (managed `ClusterBinding` mirroring `exportedAPIs`). |
| service-account + kubeconfig minting ([backend/kubernetes/resources/](../../backend/kubernetes/resources/)) | backend layer (it's credential issuance, i.e. auth). |
| kcp integration ([contrib/kcp/](../../contrib/kcp/)) | a backend *flavor*: kcp workspaces give per-consumer isolation for free, which is exactly what identity-mapping core wants. The core already speaks to kcp-like providers natively via `schema.source: OpenAPI` (no CRDs needed); the backend flavor only adds workspace provisioning/handshake. Likely the best-fit v2 deployment. |
| heartbeat / `ClusterBinding` | core keeps a plain `Lease` on the provider; anything richer is an optional provider-side visibility component. |

GitOps is the degenerate case: a human or pipeline commits the one-apply file (Secret +
`Connection` + bindings), no optional layer at all.

### What gets deleted outright

* `Isolation` / `ClusterScopedIsolation` / `informerScope` fields and the
  isolation strategy implementations (`prefixed.go`, `namespaced.go`, `none.go`).
* `APIServiceNamespace` and the namespace-lifecycle controller.
* `BoundSchema` (core reads provider CRDs directly; the provider-side copy-of-a-CRD
  object disappears).
* `ClusterBinding` from core (possibly resurrected in the backend layer).
* Embedded OIDC, sessions, cookies, SPA from anything called "core".
* The hardcoded claimable-APIs list (`claimable_apis.go`) — replaced by
  `relatedResources` limited to `secrets`/`configmaps`, label + named selectors only.

### Repo & module layout (new group, same repo, `v2/` prefix)

One rule: **the path tells you the version**. Everything v2 lives under `v2/`; every
path outside it is v1 by definition — frozen on main, maintained on a release branch,
deleted from main at v2 GA.

```
kube-bind/
├── v2/                              # ── ALL v2 code lives here ──
│   ├── sdk/                         # Go module: type-only API (core.kube-bind.io)
│   │   └── apis/core/v1alpha1/      #   Connection, ClusterBinding, Binding
│   ├── konnector/                   # Go module: the slim core engine + binary
│   │   ├── cmd/konnector/
│   │   ├── engine/                  #   sync, schema sources (CRD/OpenAPI), conflicts
│   │   └── providers/               #   mcr consumer-side providers (single, kcp, fleet)
│   ├── backend/                     # future optional layer (follow-up proposal)
│   └── cli/                         # future optional layer (follow-up proposal)
│
├── sdk/apis/kubebind/v1alpha2/      # v1 — frozen; types kept on main for consumers
├── pkg/  cmd/  backend/  cli/  web/ # v1 — frozen on main, fixes on release-1.x,
└── contrib/kcp/                     #      deleted from main at v2 GA
```

Rules:

* **No imports across the boundary, in either direction**: nothing under `v2/` imports
  v1 packages, nothing outside `v2/` imports v2 packages. Enforced in CI. Shared code is
  copied, not linked — the freedom to diverge is the point of the split.
* `v2/sdk` is a type-only module so consumers (backends, integrations) can depend on the
  API without pulling the engine; `v2/konnector` depends on `v2/sdk`, never vice versa.
* Within v2, optional layers (`v2/backend`, `v2/cli`) depend on `v2/sdk` (and at most
  the konnector's library surface), never the other way.
* The v2 konnector binary serves **only** `core.kube-bind.io`. v1alpha2 konnector is
  maintained on the release branch until deprecation; no dual-stack binary.
* Engine implementation: built on multicluster-runtime (see
  [Engine](#engine-built-on-multicluster-runtime)); one shared informer set per provider
  connection, no hand-rolled informer plumbing.
* Images follow the same rule: `ghcr.io/kube-bind/konnector:v2.*` is built from
  `v2/konnector`; `v1.*` / `v0.*` tags only ever come from the release branch.

## Migration

* v1alpha2 and core/v1alpha1 are **parallel universes**: no conversion webhooks, no
  in-place upgrade. The semantics (isolation, namespace mapping) don't map.
* Migration tooling = a documented procedure + (maybe) a `kubectl bind migrate` helper
  that, for bindings using `None` isolation + identity-compatible layouts, generates the
  equivalent v2 objects. Anything using `Prefixed`/`Namespaced` isolation cannot migrate
  to core semantics and stays on v1 / moves to a per-consumer provider deployment first.
* v1alpha2 enters maintenance on the day v2 core reaches alpha; removal horizon TBD.

## Decided

* **One apply**: a complete e2e binding is a single `kubectl apply -f` of one file
  (Secret + `Connection` + bindings); all core objects are order-independent and
  level-triggered — missing references are `Pending` conditions, never errors.
* **Naming**: group `core.kube-bind.io`, kinds `Connection`, `ClusterBinding`, `Binding`.
  The agent stays **konnector**.
* **Binding shape & scope**: a binding lists one or more CRD names (`spec.apis`) plus
  its related resources; cluster-wide vs per-namespace is expressed by kind
  (`ClusterBinding`/`Binding`), not by fields.
* **Bind everything**: `Connection.spec.autoBind: true` — konnector maintains a managed
  `ClusterBinding` mirroring `status.exportedAPIs`. Replaces `APIServiceBindingBundle`.
* **Discovery**: provider opt-in via `core.kube-bind.io/exported=true` CRD label on
  plain Kubernetes; on CRD-less providers (kcp, kcp-like) the logical-cluster boundary
  is the export boundary (discovery minus built-ins). Exported APIs surfaced on
  `Connection.status.exportedAPIs`. No catalog CRDs in core.
* **Schema delivery**: owned by `Connection` (`source`, `pullPolicy`, `updatePolicy`).
  `source: Auto` (default) reads provider CRDs when present, else synthesizes CRDs from
  discovery + `/openapi/v3` — so kcp-like, CRD-less providers work in core.
  `pullPolicy` defaults to `Bound` — CRDs are installed only when a binding references
  them; `All` is the eager opt-in.
* **Conversion webhooks**: CRDs with `strategy: Webhook` are refused in core alpha
  (per-API condition + skip); revisit on demand.
* **Sync direction**: fixed — spec consumer→provider, status provider→consumer. A
  `syncMode` field name is reserved per-API but not implemented in alpha.
* **Related resources**: `secrets` + `configmaps` only; `labelSelector` + named
  selectors only. JSONPath reference-following selectors do not enter core.
* **Conflicts**: `conflictPolicy: Fail | Adopt` with three-way classification (no markers
  / ours / foreign-or-stale). `Adopt` only takes *un-owned* objects and never steals one
  carrying another binding's/consumer's markers — cross-binding collisions are always
  conflicts, first-writer-wins. Per-object detail lives on the conflicting object's own
  condition (`ForeignObjectExists` vs `OwnedByAnother`); the binding holds only a
  `Conflicts` condition + count. The marker's source cluster UID is the
  `Connection`-pinned identity, so conflicts stay well-defined on CRD-less providers too.
* **Cluster identity**: each `Connection` pins the resolved provider (and local) cluster
  UID in its status on first connect and is immutable thereafter; a Secret later pointing
  at a *different* cluster is rejected (`Connected=False`) rather than silently re-homing
  synced objects.
* **Deletion**: consumer deletion propagates to the provider and the binding/`Connection`
  drains object finalizers before finalizing (`DrainingObjects`); `deletion-policy:
  Orphan` opts an object out of provider-side deletion.
* **Provider namespaces**: konnector creates missing namespaces iff RBAC allows
  (annotated kube-bind-created, cleaned up on unbind); otherwise condition + wait.
* **Heartbeat**: a plain `coordination.k8s.io/Lease` per Connection, maintained by the
  konnector in a designated provider namespace. Still zero kube-bind CRDs on the
  provider.
* **Mapper**: identity only in-tree; compile-time interface for out-of-tree key mapping;
  scope conversion impossible by construction.
* **Topology**: consumer-side pull; the konnector is the **only running component** in
  the core — no backend required. Provider needs zero kube-bind CRDs, controllers, or
  processes. Provider-side credential lifecycle and dead-consumer GC are service-layer
  jobs (keyed off the Lease).
* **Engine**: built on multicluster-runtime as a bridge between two cluster sets — a
  custom mcr provider turns each `Connection` into a logical provider cluster, and the
  consumer side sits behind an mcr provider too (single-cluster in alpha; kcp provider =
  one konnector per kcp instance serving all workspaces; fleet provider = sets of
  consumer clusters to sets of providers). The sync domain is always *(consumer logical
  cluster, Connection)*; multi-consumer is an engine/deployment dimension, never an API
  dimension. Sync controllers are multi-cluster-aware reconcilers, no hand-rolled
  informer/contextstore machinery.
* **Repo**: new API group, same repo, `v2/` prefix directory — the path tells you the
  version. `v2/sdk` (types) + `v2/konnector` (engine) as separate Go modules; no imports
  across the v1/v2 boundary in either direction (CI-enforced); v1 paths frozen on main
  and deleted at v2 GA.
* **Dual-stack**: none. v2 konnector serves only `core.kube-bind.io`; the v1alpha2
  konnector is maintained on a release branch until deprecation.
* **Backend/UI/CLI redesign**: separate follow-up proposal; this doc only fixes the
  contract boundary.

## Open questions

1. **OpenAPI synthesis fidelity.** How lossy is discovery + `/openapi/v3` → CRD in
   practice (CEL rules, defaulting edge cases, multi-version with conversion)? Needs a
   spike against kcp and a kcp-like system (e.g. kplane) before the `Auto` default is
   locked. Mitigation already in the contract: the provider is the enforcing side;
   upstream rejections surface as per-object sync conditions.

   Do we need different way to deliver schema as we do now in v1?

2. **Built-ins filter for `source: OpenAPI`.** Exact exclusion list/heuristic for "minus
   built-ins" in `exportedAPIs` (core groups, `*.k8s.io`, kcp's own groups?) — and
   whether it should be overridable on the `Connection`.
