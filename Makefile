# Copyright 2026 The kbind Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# kbind slim-core. The repo root is the konnector module; sdk/ is the
# standalone API module (github.com/kbind/kbind/sdk), joined via go.work.

CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.2
GOLANGCI_LINT  ?= golangci-lint
ENVTEST_K8S_VERSION ?= 1.34.1
SETUP_ENVTEST  ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21
CHART ?= deploy/charts/konnector-v2
BACKEND_CHART ?= deploy/charts/backend-v2
IMAGE ?= ghcr.io/kbind/konnector:dev

.PHONY: all
all: codegen build

.PHONY: build
build:
	go build ./...
	cd sdk && go build ./...

.PHONY: konnector
konnector:
	go build -o bin/konnector ./cmd/konnector

.PHONY: backend
backend:
	go build -o bin/backend ./cmd/backend

.PHONY: bind
bind:
	go build -o bin/bind ./cmd/bind

# CLI release archives for all platforms via goreleaser (kubectl-bind inside,
# for the krew "bind" plugin). Local dry-run; CI runs `release` on tags.
.PHONY: cli-snapshot
cli-snapshot:
	goreleaser release --snapshot --clean

# Container images. Build context is the repo root (see Dockerfile).
.PHONY: image
image:
	docker build -t $(IMAGE) .

BACKEND_IMAGE ?= ghcr.io/kbind/backend:dev
.PHONY: image-backend
image-backend:
	docker build --target backend -t $(BACKEND_IMAGE) .

.PHONY: helm-lint
helm-lint:
	helm lint $(CHART)
	helm lint $(BACKEND_CHART) --set oidc.mock=true

# Render the charts to stdout for review (extra flags via HELM_ARGS=...).
.PHONY: helm-template
helm-template:
	helm template konnector $(CHART) -n kbind $(HELM_ARGS)
	helm template backend $(BACKEND_CHART) -n kbind --set oidc.mock=true $(HELM_ARGS)

# Refresh the charts' bundled CRDs from the generated sdk CRDs.
.PHONY: helm-sync-crds
helm-sync-crds: codegen
	cp sdk/config/crd/core.kbind.io_*.yaml $(CHART)/files/crds/
	cp sdk/config/crd/catalog.kbind.io_*.yaml sdk/config/crd/iam.kbind.io_*.yaml $(BACKEND_CHART)/files/crds/

.PHONY: codegen
codegen:
	cd sdk && $(CONTROLLER_GEN) object paths=./apis/...
	cd sdk && $(CONTROLLER_GEN) crd paths=./apis/... output:crd:dir=./config/crd
	cp sdk/config/crd/core.kbind.io_*.yaml pkg/konnectorinstall/manifests/crds/
	cp sdk/config/crd/catalog.kbind.io_*.yaml sdk/config/crd/iam.kbind.io_*.yaml pkg/servicecrds/crds/

# Unit tests only. The envtest-based e2e (which needs KUBEBUILDER_ASSETS) is a
# separate target so `make test` runs with no external setup.
.PHONY: test
test:
	go test $$(go list ./... | grep -v /test/e2e)
	cd sdk && go test ./...

# Run the envtest-based e2e (two in-process API servers + the engine).
.PHONY: test-e2e
test-e2e:
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test ./test/e2e/... -count=1 -timeout 600s

# Full kind-based e2e: build the image, load it into kind, helm-install the
# konnector, and assert the bind/sync flow end to end. Pass KEEP=1 to leave the
# clusters running, NO_BUILD=1 to reuse an already-loaded image.
.PHONY: test-e2e-kind
test-e2e-kind:
	IMAGE=$(IMAGE) ./hack/e2e.sh

.PHONY: vet
vet:
	go vet ./...
	cd sdk && go vet ./...

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./...
	cd sdk && $(GOLANGCI_LINT) run ./...

.PHONY: tidy
tidy:
	GOWORK=off go mod tidy
	cd sdk && GOWORK=off go mod tidy

# Generate Apache license headers on source files.
.PHONY: generate-boilerplate
generate-boilerplate:
	python3 hack/generate_boilerplate.py --boilerplate-dir=hack/boilerplate

# Verify Apache license headers on source files.
.PHONY: verify-boilerplate
verify-boilerplate:
	python3 hack/verify_boilerplate.py --boilerplate-dir=hack/boilerplate --skip docs

.PHONY: verify
verify: vet verify-boilerplate

.PHONY: demo
demo:
	./hack/demo.sh

# Tilt dev loop: two kind clusters (provider backend + consumer konnector),
# seeded catalog, rebuild-on-change — one command.
.PHONY: tilt
tilt:
	./hack/tilt/kind.sh
	# --stream: no interactive HUD (make provides no TTY); the web UI is on
	# http://localhost:10350 regardless.
	cd hack/tilt && tilt up --stream

# One-shot variant: deploy everything, wait until healthy, exit (CI-style).
.PHONY: tilt-ci
tilt-ci:
	./hack/tilt/kind.sh
	cd hack/tilt && tilt ci

# Tear the dev loop down: delete both kind clusters.
.PHONY: tilt-down
tilt-down:
	kind delete cluster --name kbind-provider || true
	kind delete cluster --name kbind-consumer || true

# Helm publishing parameters
HELM ?= helm
HELM_REPO ?= ghcr.io/kbind/charts
VERSION ?= 0.0.0-dev
CHART_VERSION ?= $(VERSION)
IMAGE_VERSION ?= $(VERSION)
HELM_CHARTS ?= konnector-v2 backend-v2

## helm-push: Package and push Helm charts to $(HELM_REPO) as OCI artifacts
.PHONY: helm-push
helm-push:
	mkdir -p bin/charts
	@for chart in $(HELM_CHARTS); do \
	  echo "==> packaging $$chart $(CHART_VERSION)"; \
	  $(HELM) package deploy/charts/$$chart \
	    --version $(CHART_VERSION) \
	    --app-version $(IMAGE_VERSION) \
	    --destination bin/charts || exit 1; \
	  echo "==> pushing $$chart-$(CHART_VERSION).tgz to oci://$(HELM_REPO)"; \
	  $(HELM) push bin/charts/$$chart-$(CHART_VERSION).tgz oci://$(HELM_REPO) || exit 1; \
	done
