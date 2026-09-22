---
description: >
  How to setup a development environment for contributing to kube-bind.
title: Development Environments
---

# Development Environments

Due to the fact that kube-bind is by nature a multi-cluster system, for development purposes it's recommended to have multiple clusters running or use kcp to simulate multiple clusters. Below are instructions for both approaches.

All the instructions assume you have already cloned the kube-bind repository and have Go installed.

* You can use [kcp](kcp.md) for a lightweight backend system.
* You can also use [kind](kind.md) for a more full-featured local Kubernetes cluster.

For v2, check out `v2-next` and use the Go version in `go.mod`:

```bash
git clone --branch v2-next https://github.com/kbind-dev/kbind.git
cd kbind
make build
make konnector backend bind
```

The root module references `sdk/` locally, no workspace file is required. See [Testing changes](../testing-changes.md) for the unit and envtest suites.
