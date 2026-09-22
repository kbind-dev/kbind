---
description: >
  Get started with kbind.
---

# Quickstart

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)
- [kubectl bind plugin](kubectl-plugin.md) installed

## Start with kbind

### Quick Development Setup

For a local development environment, follow the [kind setup guide](../developers/dev-environment/kind.md). It creates a provider cluster and a consumer cluster, starts the backend and konnector, and adds the Widget API to the catalog. Docker, kind, and Go are required.

After the development environment is ready, you can:

1. **Authenticate to the provider cluster:**

    ```bash
    kubectl bind login http://localhost:8080
    ```

    This opens a browser for authentication and saves the session.

2. **Bind an API service from provider to consumer:**

    ```bash
    kubectl bind catalog
    kubectl bind export widgets --kubeconfig=.kbind-quickstart/consumer.kubeconfig \
      --install-konnector=false
    ```

    The development setup already runs the konnector, so skip installing another copy. You can also browse the catalog at `http://localhost:8080`.

3. **Verify bound resources:**

    ```bash
    export KUBECONFIG="$PWD/.kbind-quickstart/consumer.kubeconfig"
    kubectl get connections,clusterbindings
    kubectl wait --for=condition=Established crd/widgets.example.org --timeout=120s
    ```

4. **Create an example resource:**

    ```bash
    kubectl apply -f - <<'EOF'
    apiVersion: example.org/v1
    kind: Widget
    metadata:
      name: my-widget
      namespace: default
    spec:
      size: large
    EOF
    ```

5. **Check the resource synced to the provider:**

    ```bash
    kubectl --kubeconfig=.kbind-quickstart/provider.kubeconfig -n default \
      wait --for=create widget/my-widget --timeout=120s
    kubectl --kubeconfig=.kbind-quickstart/provider.kubeconfig -n default get widgets
    ```

For other service APIs, see the [integration examples](../usage/integrations/index.md). When finished, follow the development guide's [cleanup](../developers/dev-environment/kind.md#cleanup).

### Production Deployment

For an in-cluster deployment, use the [Helm charts](helm.md) to install the consumer konnector and optional provider backend.

Once you have a backend running, either locally or in a cluster, you can connect to it:

### Connect to kbind Server

```bash
kubectl bind login https://my-kube-bind-server.example.com
```

### Open kbind Web UI and bind services

Open your provider's URL in a browser to browse the catalog and bind services. For the same workflow from the CLI, use `kubectl bind catalog` and `kubectl bind export <name>`.
