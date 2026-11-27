---
description: >
  Install and use the kubectl bind plugin.
---

# kubectl bind Plugin

The `kubectl bind` plugin is the command-line interface for interacting with kbind services. It connects to remote service providers, browses their catalogs, and binds APIs into your cluster.

## Installation

=== "Krew"

    [Krew](https://krew.sigs.k8s.io/) installs released versions of the plugin, not this unreleased v2 preview. Use **Manual Build** for this preview rather than installing a 0.x release.

=== "Manual Build"

    Build and install from source using the Go version declared in `go.mod`:

    ```bash
    git clone --branch v2-next https://github.com/kbind-dev/kbind.git
    cd kbind
    make bind
    mkdir -p "$HOME/.local/bin"
    install -m 0755 bin/bind "$HOME/.local/bin/kubectl-bind"
    export PATH="$HOME/.local/bin:$PATH"
    kubectl bind --help
    ```

    Keep `$HOME/.local/bin` on your shell's `PATH`. Installing the build output as `kubectl-bind` makes it available as `kubectl bind`.

=== "Binary Download"

    Published CLI archives are available on the [releases page](https://github.com/kbind-dev/kbind/releases). Choose a v2 release explicitly, not the latest 0.x release. For this unreleased branch, use **Manual Build**.

    After extracting an archive for your operating system and architecture, install its `kubectl-bind` binary:

    ```bash
    mkdir -p "$HOME/.local/bin"
    install -m 0755 kubectl-bind "$HOME/.local/bin/kubectl-bind"
    export PATH="$HOME/.local/bin:$PATH"
    ```

## Basic Usage

With the [v2 konnector installed](helm.md) in your consumer cluster:

```bash
kubectl bind login https://my-kube-bind-server.example.com
kubectl bind catalog
kubectl bind export widgets --kubeconfig=./consumer.kubeconfig --install-konnector=false
kubectl --kubeconfig=./consumer.kubeconfig get connections,clusterbindings
```

Replace `widgets` with an Export from the catalog. To use the Web UI, open the provider URL in your browser. In v2, binding uses `export <name>` rather than bare `kubectl bind`.

See the [CLI reference](../reference/cli/index.md) for all commands and the [catalog guide](../usage/catalog.md) for sessions, credentials, and bundle handling.

## Quick Start

1. Build and install the v2 plugin using **Manual Build**.
2. Install the consumer konnector using the [Helm guide](helm.md).
3. Log in to your provider with `kubectl bind login <server-url>`.
4. Browse with `kubectl bind catalog` and bind an Export with `kubectl bind export <name>`.

For a local example, follow the [Quickstart Guide](quickstart.md).
