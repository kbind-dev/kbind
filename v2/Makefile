# Copyright 2026 The Kube Bind Authors. Licensed under the Apache License 2.0.
#
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

.PHONY: codegen
codegen:
	cd sdk && $(CONTROLLER_GEN) object paths=./apis/...
	cd sdk && $(CONTROLLER_GEN) crd paths=./apis/... output:crd:dir=./config/crd

ENVTEST_K8S_VERSION ?= 1.34.1
SETUP_ENVTEST ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21

.PHONY: test
test:
	cd konnector && go test ./...
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
