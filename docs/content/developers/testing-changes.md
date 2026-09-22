---
description: >
  How to test changes made to kbind in your development environment.
title: Testing Changes
---

# Testing code changes

Use the v2 source branch rather than `main`, which still describes the 0.x implementation:

```bash
git clone --branch v2-next https://github.com/kbind-dev/kbind.git
cd kbind
make build
make konnector backend bind
```

The root module builds the engine, backend, and CLI. `sdk/` is a separate Go module with a local `replace` in the root `go.mod`, a `go.work` file is not required. Use the Go version declared in `go.mod` (currently 1.26.2). The binaries are `bin/konnector`, `bin/backend`, and `bin/bind`.

## Run a change locally

Start with the [two-cluster walkthrough](dev-environment/kind.md). Running the konnector on your workstation against the consumer kubeconfig makes engine changes easy to iterate without rebuilding an image.

The [backend setup](backend/index.md) covers a local gateway with mock OIDC. Never use mock authentication on an exposed production endpoint. The gateway UI is embedded from `web/`, the v2 UI does not require the old npm frontend build.

For in-cluster runs, build and load or push explicitly tagged images:

```bash
make image IMAGE=ghcr.io/kbind-dev/konnector:dev
make image-backend BACKEND_IMAGE=ghcr.io/kbind-dev/backend:dev
```

These commands build locally, they do not publish the images. Use the `deploy/charts/konnector-v2` and `deploy/charts/backend-v2` charts and override their image tags to match. The legacy chart directory names without `-v2` do not exist in this branch.

## Tests

```bash
make test
make test-e2e
```

Unit tests run without external Kubernetes setup. The envtest suite starts two in-process Kubernetes API servers and the real engine controllers. `make test-e2e` obtains API-server binaries through `setup-envtest`.

Run related tests together when iterating:

```bash
KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21 use 1.34.1 -p path)" \
  go test ./test/e2e -run 'TestSlimCore(HappyCase|NamespacedBinding)$' \
  -count=1 -timeout=600s
```

The suite covers schema policies, OpenAPI and kcp-like discovery, related resources, conflicts, cleanup, and stop-on-disengage. `TestBackendFullLoop` adds gateway issuance, bundle pickup, provider-fenced credentials, heartbeat, and revocation. Envtest is not a real provider operator or a full kcp deployment.

The repository also has `make test-e2e-kind` for a containerized run with Docker, kind, Helm, and kubectl. The Tilt helper still refers to the old chart directory names, use the step-by-step quickstart instead of relying on `make tilt` for this preview.

### Demo scripts

`make demo` runs `hack/demo.sh`, which creates or reuses `kbind-provider` and `kbind-consumer` kind clusters and writes administrator kubeconfigs under `/tmp`. It prints a command for running the konnector on your workstation.


`hack/e2e.sh` builds and loads an image, installs the konnector chart, and deletes its named clusters on exit unless `KEEP=1`. Inspect it before use. Its `NAMESPACE` override does not rewrite the fixed `kbind` Secret reference in `config/samples/binding.yaml`. For explicit cluster selection and manual cleanup, use the [kind walkthrough](dev-environment/kind.md).

## API changes

Edit types in `sdk/apis/{core,catalog,iam}/v1alpha1`, then regenerate the deep-copy code, CRDs, and packaged manifests:

```bash
make helm-sync-crds
```

This includes the core CRDs embedded by `pkg/konnectorinstall`, service CRDs in `pkg/servicecrds`, and both charts' copies. Run `make generate-docs` to refresh local API and CLI references.

Release tags publish images and OCI Helm charts with explicit versions. Final tags also trigger CLI release artifacts, prerelease tags do not. See the [release procedure](https://github.com/kbind-dev/kbind/blob/v2-next/docs/RELEASING.md).
