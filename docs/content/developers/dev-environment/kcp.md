---
description: >
  How to use a kcp provider with the v2 core.
title: kcp
---

# Development Environment using kcp

All the instructions assume you have already cloned the kbind repository and have Go installed.

kcp requires initial setup before it can be used. This includes setting up the provider workspace and its `APIResourceSchemas` and `APIExports`. This is not required if you control the setup with your own scripts.

It's useful to have the kcp CLI installed for workspace management:

```bash
kubectl krew index add kcp-dev https://github.com/kcp-dev/krew-index.git
kubectl krew install kcp-dev/kcp
kubectl krew install kcp-dev/ws
kubectl krew install kcp-dev/create-workspace
```

## Preparation

Start kcp and populate the provider workspace using the [kcp documentation](https://docs.kcp.io/). v2 does not include the old `make run-kcp` or `kcp-init` bootstrap helpers.

The workspace should expose only the service APIs intended for consumers. Give the provider credential discovery/OpenAPI access, the required instance permissions, and read access to `logicalclusters.core.kcp.io/cluster`.

## Provider

### Kubeconfig Setup

Use a self-contained kubeconfig pointing at the selected logical cluster. Confirm the workspace identity:

```bash
kubectl --kubeconfig=provider.kubeconfig get logicalcluster cluster
```

The konnector pins that LogicalCluster UID. On a CRD-less provider, choose `schema.source: OpenAPI` explicitly, the current `Auto` path does not fall back when CRD listing returns an error.

## Consumer

### Initialization

Install the konnector and its three core CRDs on a Kubernetes consumer using the [installation guide](../../setup/helm.md). Deliver the provider kubeconfig as a Secret in the consumer's `kbind` namespace.

### Binding

```yaml
apiVersion: core.kbind.io/v1alpha1
kind: Connection
metadata:
  name: workspace-provider
spec:
  kubeconfigSecretRef:
    namespace: kbind
    name: workspace-provider
    key: kubeconfig
  schema:
    source: OpenAPI
    pullPolicy: Bound
    updatePolicy: Always
```

Add a Binding or ClusterBinding selecting the actual `<plural>.<group>` names in that workspace. See [API concepts](../../usage/api-concepts.md) for the manifest and [schema limits](../../usage/api-concepts.md#schema-updates-and-fidelity) before relying on synthesized validation or multi-version behavior.

### Launch Konnector

For a host-run development process rather than a Pod:

```bash
KUBECONFIG=consumer.kubeconfig go run ./cmd/konnector
```

The core preserves namespace, name, and scope. It does not provision workspaces or translate cluster-scoped APIs into namespaced APIs. The optional backend's in-tree issuer is Kubernetes-specific, its former multicluster-provider flags are not part of the v2 backend.
