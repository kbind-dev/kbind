# Cluster Binding

In v2, connecting a consumer has two separate operations: install the konnector, then apply a core bundle that establishes a provider Connection. There is no separate cluster-registration handshake or stored consumer kubeconfig.

## Install the konnector

`GET /api/konnector` returns the core CRDs, namespace, RBAC, ServiceAccount, and Deployment without credentials. It is public and has no server-side write effects. Installing these objects alone does not make the consumer appear in the provider inventory.

## Connect a provider

The [API binding flow](api-binding.md) returns a Secret, Connection, and ClusterBinding. After application, the konnector connects directly to the provider and renews a heartbeat Lease.

`GET /api/clusters` builds the inventory from these Leases and Grants. It is authenticated, but its output is not filtered to the caller's identity.

When explicitly enabled, `POST /api/apply` can install the konnector or apply a bundle using a submitted consumer kubeconfig. This sends consumer credentials through the gateway, local CLI or GitOps apply does not.

See the [HTTP reference](index.md) for request bodies and responses.
