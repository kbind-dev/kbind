# Tests

This is the test catalog for the kbind slim core: every scenario, what it
asserts, and what is **not** covered yet. Keep it in sync when adding tests.

## How the tests run

- **e2e** (`test/e2e/`) run on **envtest** — real `kube-apiserver` + `etcd`
  binaries as local processes (no kind, no Docker, no kubelet/nodes). Each test
  starts **two** control planes: a *consumer* (pre-loaded with the core CRDs) and
  a *provider*, and runs the real engine reconcilers in-process against the
  consumer. See [e2e/framework/framework.go](e2e/framework/framework.go).
- **unit** tests live next to the code under `engine/*/` and need no cluster.

```sh
make test        # unit tests only (no external setup)
make test-e2e    # downloads envtest assets and runs the e2e suite

# one e2e test, verbose:
KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21 use 1.34.1 -p path)" \
  go test -v ./test/e2e -run TestSlimCoreHappyCase
```
