# Copyright 2026 The Kube Bind Authors.
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
CHART ?= deploy/charts/konnector
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

# Container image. Build context is the repo root (see Dockerfile).
.PHONY: image
image:
	docker build -t $(IMAGE) .

.PHONY: helm-lint
helm-lint:
	helm lint $(CHART)

# Render the chart to stdout for review (extra flags via HELM_ARGS=...).
.PHONY: helm-template
helm-template:
	helm template konnector $(CHART) -n kbind $(HELM_ARGS)

# Refresh the chart's bundled CRDs from the generated sdk CRDs.
.PHONY: helm-sync-crds
helm-sync-crds: codegen
	cp sdk/config/crd/core.kbind.io_*.yaml $(CHART)/files/crds/

.PHONY: codegen
codegen:
	cd sdk && $(CONTROLLER_GEN) object paths=./apis/...
	cd sdk && $(CONTROLLER_GEN) crd paths=./apis/... output:crd:dir=./config/crd

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

# Verify Apache license headers on source files.
.PHONY: verify-boilerplate
verify-boilerplate:
	python3 hack/verify_boilerplate.py --boilerplate-dir=hack/boilerplate --skip docs

.PHONY: verify
verify: vet verify-boilerplate

.PHONY: demo
demo:
	./hack/demo.sh
