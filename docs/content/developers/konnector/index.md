# konnector

The konnector runs on the consumer and synchronizes APIs from provider clusters. In v2 it is the only running component required by the core.

- [Konnector](controllers/konnector.md): provider engagement, managers, and runtime options.
- [Connection controller](controllers/connection.md): credentials, identity, discovery, and heartbeat.
- [Binding controllers](controllers/binding.md): API selection, schema delivery, and cleanup.
- [Synchronization](controllers/synchronization.md): per-API spec/status watches and ownership.

See [Architecture](../architecture.md) for the package layout.

## Deployment

Start with [Installation with Helm](../../setup/helm.md). The konnector runs in the consumer cluster and connects directly to provider Kubernetes APIs. It needs no kbind controller or kbind CRDs on a plain Kubernetes provider.

### Local kind images

For an existing kind consumer, build and load the image rather than pushing it:

```bash
export IMAGE_REPOSITORY=kbind-local/konnector
export IMAGE_TAG="git-$(git rev-parse --short=12 HEAD)"
make image IMAGE="$IMAGE_REPOSITORY:$IMAGE_TAG"
kind load docker-image "$IMAGE_REPOSITORY:$IMAGE_TAG" --name consumer
```

Use those values with the Helm command and select `kind-consumer` as its context. Build for the consumer nodes' architecture and use a new tag for uncommitted changes. For a private registry, configure `imagePullSecrets`.

The Pod must be able to reach each provider endpoint using the CA and credentials in its Connection Secret. A workstation's `127.0.0.1` kind endpoint is not reachable from a consumer Pod. With two kind clusters on the same Docker network, use an address reachable across that network and covered by the provider serving certificate. Keep TLS verification enabled.

### Chart configuration

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository`, `image.tag` | repository plus placeholder appVersion | Set both explicitly for this branch. |
| `installCRDs` | `true` | Install the three core CRDs as Helm templates. |
| `replicaCount` | `1` | Number of Pods, only one should actively reconcile. |
| `leaderElect` | `false` | Automatically forced on when `replicaCount > 1`. |
| `leaderElectionID` | `konnector.kbind.io` | Consumer-side leader-election Lease name. |
| `rbac.boundResourceGroups` | `["*"]` | Consumer API groups the engine can synchronize. |
| `metrics.port` | `8085` | Controller-runtime metrics endpoint. |
| `healthProbe.port` | `8081` | `/healthz` and `/readyz` endpoints. |
| `extraArgs` | `[]` | Additional konnector arguments, including logging flags. |

For multiple replicas, the chart supplies leader-election RBAC and enables election. Do not deploy independent active releases against the same consumer. The probes are process checks, not proof that a Connection or instance is syncing.

## RBAC and credentials

### Consumer permissions

The chart manages core objects and their status/finalizers, CRDs, credential Secrets, namespaces, and Events. By default, it also grants ordinary CRUD/watch operations on **all API groups and resources**. Review this broad access before a shared deployment.

For example, narrow the configurable resource rule to Widget APIs:

```yaml
rbac:
  boundResourceGroups:
    - example.org
```

Pass the values with `helm ... -f your-values.yaml`. This does not narrow the chart's separate access to core kbind objects, CRDs, or credential Secrets.

For related resources, supply the additional consumer RBAC they need. The chart's fixed Secret rule allows reads and updates for credentials, but not creation/deletion of related Secret copies. There is no fixed ConfigMap rule. Add narrowly scoped rules for the related resource kinds and direction rather than granting all core resources through the empty API group.

A namespaced Binding restricts which instances synchronize, not informer permissions. Current instance informers watch cluster-wide on both sides, so a namespace-only Role is not sufficient.

### Provider permissions

Use dedicated credentials for the intended provider boundary, not a production administrator kubeconfig.

| Operation | Required provider access |
| --- | --- |
| Identify plain Kubernetes | Read Namespace `kube-system`. |
| Identify a kcp logical cluster | Read `logicalclusters.core.kcp.io/cluster`, a forbidden result is not ignored. |
| Discover CRD exports | List CRDs and get selected CRDs in `apiextensions.k8s.io`. |
| Discover OpenAPI exports | Read discovery endpoints and `/openapi/v3` documents. |
| Sync instances | Cluster-wide get/list/watch for the selected APIs, create/patch/update/delete for targets. |
| Materialize a namespace | Create namespaces, the engine attempts a create even if the namespace may already exist. |
| Sync related objects | List/read selected sources, read/list/create/patch/delete targets according to direction. |
| Heartbeat | Get/create/update Leases in the kubeconfig context namespace, or `kbind` when unset. |

Provider RBAC and network boundaries control access. Export labels are not authorization. Namespace/name mapping is identity-only, credentials with a default namespace do not redirect objects into it.

Store the provider kubeconfig in a consumer Secret and reference its explicit namespace/name/key from a Connection. The chart does not create credentials. See [API concepts](../../usage/api-concepts.md#credentials-and-cluster-identity) for immutable references, identity pinning, and rotation.

## Manage CRDs outside Helm

For a GitOps-managed lifecycle, install the core manifests before the release:

```bash
kubectl --context="$CONSUMER_CONTEXT" apply \
  -f sdk/config/crd/core.kbind.io_connections.yaml \
  -f sdk/config/crd/core.kbind.io_clusterbindings.yaml \
  -f sdk/config/crd/core.kbind.io_bindings.yaml
```

Set `installCRDs: false` from the first Helm installation. Do not switch an existing release from `true` to `false` without planning the migration. These are rendered templates, not Helm's retained `crds/` directory, so removing them from a release can delete them and their custom resources.

The rest of `sdk/config/crd` contains optional catalog and IAM CRDs. Installing the three named files keeps a core-only deployment minimal.

Before uninstalling, unbind while the controller and credentials still work, delete Connections after bindings drain, and remove credential Secrets after their finalizers are released. Removing the controller or CRDs first bypasses normal teardown. See [Unbinding and deletion](../../usage/synchronization.md#unbinding-and-deletion).
