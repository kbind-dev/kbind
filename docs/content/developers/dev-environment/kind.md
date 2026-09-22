---
description: >
  Set up a local provider and consumer for kbind development.
title: kind
---

# Development Environment using kind

This guide sets up a provider backend, a consumer konnector, and a Widget catalog offering for the [Quickstart](../../setup/quickstart.md). Both processes run on your workstation against two kind clusters.

## Pre-requisites

- [Docker](https://docs.docker.com/get-docker/), [kind](https://kind.sigs.k8s.io/), and [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl).
- A v2 source checkout and Go matching `go.mod`.
- The [kubectl bind plugin](../../setup/kubectl-plugin.md) installed.

Use disposable clusters. The kubeconfigs below contain administrator credentials, keep them out of version control and remove them after the demo. Run commands from the repository root.

## Setup

Create the two clusters and save their kubeconfigs:

```bash
kind create cluster --name provider
kind create cluster --name consumer

kind get kubeconfig --name provider > provider.kubeconfig
kind get kubeconfig --name consumer > consumer.kubeconfig
make backend konnector
```

These kubeconfigs use workstation endpoints. They work for the host-run processes below, not for konnector Pods in another cluster.

## Provider

In a second terminal, start the backend:

```bash
KUBECONFIG="$PWD/provider.kubeconfig" ./bin/backend \
  --oidc-mock --external-url=http://localhost:8080
```

Leave it running. Mock OIDC automatically approves logins and is for this local development environment only. The backend installs its catalog and IAM CRDs.

Back in the first terminal, wait for the catalog APIs and seed the example:

```bash
kubectl --kubeconfig=provider.kubeconfig \
  wait --for=condition=Established --timeout=120s \
  crd/exports.catalog.kbind.io crd/collections.catalog.kbind.io
kubectl --kubeconfig=provider.kubeconfig \
  apply -f hack/tilt/seed/widgets.yaml
kubectl --kubeconfig=provider.kubeconfig \
  wait --for=condition=Ready exports.catalog.kbind.io/widgets --timeout=120s
```

The manifest installs the Widget CRD, related Secret and ConfigMap, an Export, and a Collection. It does not include a Widget controller.

## Consumer

Install the core CRDs:

```bash
kubectl --kubeconfig=consumer.kubeconfig apply \
  -f sdk/config/crd/core.kbind.io_connections.yaml \
  -f sdk/config/crd/core.kbind.io_clusterbindings.yaml \
  -f sdk/config/crd/core.kbind.io_bindings.yaml
kubectl --kubeconfig=consumer.kubeconfig \
  wait --for=condition=Established --timeout=120s \
  crd/connections.core.kbind.io crd/clusterbindings.core.kbind.io \
  crd/bindings.core.kbind.io
```

In a third terminal, start the konnector:

```bash
KUBECONFIG="$PWD/consumer.kubeconfig" ./bin/konnector
```

Leave it running. Do not also install a konnector Deployment on this consumer.

## Login and bind a service

The environment is ready:

| Setting | Value |
| --- | --- |
| Provider kubeconfig | `provider.kubeconfig` |
| Consumer kubeconfig | `consumer.kubeconfig` |
| Backend URL | `http://localhost:8080` |
| Catalog Export | `widgets` |

Return to the [Quickstart](../../setup/quickstart.md#quick-development-setup) to log in, bind the Widget API, and create an instance.

The example Secret and ConfigMap are copied from the provider after binding. For instance status updates, simulate the service controller:

```bash
kubectl --kubeconfig=provider.kubeconfig -n default \
  patch widget my-widget --subresource=status --type=merge \
  -p '{"status":{"phase":"Running"}}'
kubectl --kubeconfig=consumer.kubeconfig -n default \
  wait --for=jsonpath='{.status.phase}'=Running widget/my-widget --timeout=120s
```

See [Synchronization](../../usage/synchronization.md) for spec, status, related resources, and unbinding behavior.

## Cleanup

Stop both host processes with Ctrl-C. Remove only the disposable clusters and credentials created for this walkthrough:

```bash
kind delete cluster --name consumer
kind delete cluster --name provider
rm provider.kubeconfig consumer.kubeconfig
unset KUBECONFIG
```

For shared clusters, [unbind](../../usage/synchronization.md#unbinding-and-deletion) while the konnector and credentials still work instead of deleting the clusters.
