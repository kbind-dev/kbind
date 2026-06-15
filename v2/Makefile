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

# v2 slim-core. Run from the v2/ directory.

CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.2

.PHONY: all
all: codegen build

.PHONY: build
build:
	cd konnector && go build ./...
	cd sdk && go build ./...

.PHONY: konnector
konnector:
	cd konnector && go build -o bin/konnector ./cmd/konnector

# Container image. Build context is v2/ so the konnector's `replace ../sdk` resolves.
IMAGE ?= ghcr.io/kube-bind/konnector:dev
.PHONY: image
image:
	docker build -f konnector/Dockerfile -t $(IMAGE) .

CHART ?= konnector/deploy/charts/konnector
.PHONY: helm-lint
helm-lint:
	helm lint $(CHART)

# Render the chart to stdout for review (extra flags via HELM_ARGS=...).
.PHONY: helm-template
helm-template:
	helm template konnector $(CHART) -n kube-bind $(HELM_ARGS)

# Refresh the chart's bundled CRDs from the generated sdk CRDs.
.PHONY: helm-sync-crds
helm-sync-crds: codegen
	cp sdk/config/crd/core.kube-bind.io_*.yaml $(CHART)/files/crds/

.PHONY: codegen
codegen:
	cd sdk && $(CONTROLLER_GEN) object paths=./apis/...
	cd sdk && $(CONTROLLER_GEN) crd paths=./apis/... output:crd:dir=./config/crd

ENVTEST_K8S_VERSION ?= 1.34.1
SETUP_ENVTEST ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21

# Unit tests only. The envtest-based e2e (which needs KUBEBUILDER_ASSETS) is a
# separate target so `make test` runs with no external setup.
.PHONY: test
test:
	cd konnector && go test $$(go list ./... | grep -v /test/e2e)
	cd sdk && go test ./...

# Run the envtest-based e2e (two in-process API servers + the engine).
.PHONY: test-e2e
test-e2e:
	cd konnector && KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test ./test/e2e/... -count=1 -timeout 600s

.PHONY: vet
vet:
	cd konnector && go vet ./...
	cd sdk && go vet ./...

.PHONY: tidy
tidy:
	cd konnector && GOWORK=off go mod tidy
	cd sdk && GOWORK=off go mod tidy

.PHONY: demo
demo:
	./hack/demo.sh
