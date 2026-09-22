---
description: >
  Install kbind on an existing Kubernetes cluster via the Helm charts.
---

# Installation with Helm

The konnector chart runs in the consumer cluster. The optional backend chart provides a catalog, authentication, and credentials on the provider.

## Prerequisites

- A Kubernetes consumer cluster and Helm 3.
- A v2 source checkout and an image built from that checkout.
- A provider API reachable from the consumer cluster.

This preview uses the local charts. Do not use a 0.x chart or assume a `latest` image contains v2.

## Install the Konnector

From the repository root, build and push an image to a registry you control:

```bash
export IMAGE_REPOSITORY=registry.example.com/your-project/konnector
export IMAGE_TAG="git-$(git rev-parse --short=12 HEAD)"
make image IMAGE="$IMAGE_REPOSITORY:$IMAGE_TAG"
docker push "$IMAGE_REPOSITORY:$IMAGE_TAG"
```

Replace the registry above. Use a new tag when rebuilding uncommitted changes. For local kind clusters, load the image with `kind load docker-image` instead.

Install into your consumer context:

```bash
export CONSUMER_CONTEXT=kind-consumer
helm upgrade --install konnector ./deploy/charts/konnector-v2 \
  --kube-context "$CONSUMER_CONTEXT" \
  --namespace kbind --create-namespace \
  --set-string image.repository="$IMAGE_REPOSITORY" \
  --set-string image.tag="$IMAGE_TAG" \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 180s
```

The chart installs the three core CRDs, RBAC, and the konnector Deployment. Review its broad default permissions before using a shared cluster. Provider credentials belong in a consumer Secret referenced by a Connection.

See [API concepts](../usage/api-concepts.md) for binding manifests and the [konnector guide](../developers/konnector/index.md) for configuration, networking, and credential permissions.

## Install the Backend

Service providers can use `deploy/charts/backend-v2`. Follow the [backend setup](../developers/backend/index.md) for image selection, OIDC, session-key Secrets, and Helm values, then [publish an offering](../usage/catalog.md#publish-an-offering).

The backend is optional. The core can use provider credentials and bindings directly without it.

## Upgrade and Uninstall

Repeat `helm upgrade --install` with the new image tag to upgrade. Before uninstalling, [unbind resources](../usage/synchronization.md#unbinding-and-deletion) while the konnector and provider credentials still work. With `installCRDs: true`, Helm uninstall also removes the core CRDs. See [CRD lifecycle](../developers/konnector/index.md#manage-crds-outside-helm) before changing who manages them.
