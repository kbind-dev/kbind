# kube-bind v2 — slim core (POC)

This directory is the v2 "slim core" implementation. See
[../docs/proposals/v2-slim-core.md](../docs/proposals/v2-slim-core.md) for the design.

Everything v2 lives under `v2/`; nothing here imports v1 packages and nothing
outside imports v2 (the path tells you the version).

## Layout

```
v2/
├── go.work                 # ties the two modules for local dev
├── sdk/                    # module: type-only API (core.kube-bind.io)
│   └── apis/core/v1alpha1/ #   Connection, ClusterBinding, Binding
│   └── config/crd/         #   generated CRD manifests
├── konnector/              # module: the slim sync engine + binary
│   ├── cmd/konnector/      #   main: local mgr + mcmanager + reconcilers
│   └── engine/
│       ├── provider/       #   mcr provider: Connection -> engaged cluster
│       ├── connection/     #   resolve secret, pin identity, discover exports
│       ├── binding/        #   validate + pull CRDs (schema.source: CRD)
│       ├── sync/           #   per-GVR spec-up / status-down + conflicts
│       └── remote/         #   kubeconfig + cluster identity helpers
└── hack/demo.sh            # two-kind-cluster end-to-end demo
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
- The provider side is the **multicluster-runtime engaged cluster** for each
  Connection: writes go through its client, fresh reads through its API reader,
  and status/drift events arrive via a **watch on its cache** (event-driven, not
  polled — a low-frequency resync is only a backstop).
- **Order-independent apply**: a `Connection` created before its Secret resolves
  when the Secret arrives (the konnector watches referenced Secrets); a binding
  created before its Connection resolves when the Connection goes Ready.
- **Complete unbind**: `Connection` and bindings carry a cleanup finalizer.
  Deleting a `ClusterBinding` deletes the provider copies of synced instances,
  releases instance finalizers, and removes the pulled CRD (cascading the
  instances). A `Connection` blocks (`DrainingBindings`) until its bindings are
  gone, and keeps its Secret alive (via a finalizer) so teardown can still reach
  the provider — so `kubectl delete -f bundle.yaml` is order-don't-care.

Known POC simplifications (tracked against the proposal): `schema.source:
OpenAPI` (the kcp/CRD-less path), related resources, the `Mapper` extension,
and the provider `Lease` are not implemented yet.

## Build

```sh
cd v2/konnector && go build ./...      # workspace mode (go.work)
```

## Codegen (after editing types)

```sh
cd v2/sdk
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.2 object paths=./apis/...
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.2 crd paths=./apis/... output:crd:dir=./config/crd
```

## Tests

End-to-end test ([konnector/test/e2e](konnector/test/e2e)) runs two in-process
**envtest** API servers (provider + consumer) with the real engine reconcilers, and
mirrors the v1 happy-case step pattern: Connection Ready + discovery → ClusterBinding
Ready + CRD pull → spec up → status down → spec update → conflict (foreign object not
overwritten) → deletion (incl. the foreign-object guard).

```sh
make test-e2e         # downloads envtest assets and runs the suite
```

## Run the demo (two kind clusters)

```sh
./hack/demo.sh        # creates two kind clusters and wires the bundle
# then follow the printed instructions to run the konnector and sync a Widget
```
