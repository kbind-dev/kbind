#!/usr/bin/env bash

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

# End-to-end test of the v2 slim-core konnector as a real in-cluster deployment.
#
# Unlike hack/demo.sh (which runs the konnector on the host via `go run`), this
# script exercises the shipping artifact: it builds the container image, loads
# it into kind, installs the konnector with Helm into the consumer cluster, and
# then asserts the full bind/sync flow end to end.
#
# Steps:
#   1. Create two kind clusters (provider + consumer).
#   2. Build the konnector image and load it into the consumer cluster.
#   3. Provider: install the exported Widget CRD.
#   4. Consumer: helm install the konnector (chart bundles the core CRDs).
#   5. Consumer: store the provider kubeconfig as a Secret and apply the bundle
#      (Connection + ClusterBinding).
#   6. Assert: Connection Ready, Widget CRD pulled, ClusterBinding Synced.
#   7. Assert: a Widget created on the consumer syncs UP to the provider.
#   8. Assert: status set on the provider flows DOWN to the consumer.
#
# Usage:
#   hack/e2e.sh            # full run, tears the clusters down at the end
#   KEEP=1 hack/e2e.sh     # leave the clusters and konnector running afterwards
#   NO_BUILD=1 hack/e2e.sh # reuse an already-built/loaded image (faster reruns)
#
# Env overrides: PROVIDER, CONSUMER, NAMESPACE, IMAGE, RELEASE, TIMEOUT.
set -euo pipefail

PROVIDER=${PROVIDER:-kbind-e2e-provider}
CONSUMER=${CONSUMER:-kbind-e2e-consumer}
NAMESPACE=${NAMESPACE:-kbind}
IMAGE=${IMAGE:-kbind/konnector:e2e}
RELEASE=${RELEASE:-konnector}
TIMEOUT=${TIMEOUT:-180}
KEEP=${KEEP:-0}
NO_BUILD=${NO_BUILD:-0}

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
SAMPLES="${ROOT}/config/samples"
CHART="${ROOT}/deploy/charts/konnector"
PROVIDER_KC="$(mktemp -t kbind-e2e-provider.XXXXXX)"
CONSUMER_KC="$(mktemp -t kbind-e2e-consumer.XXXXXX)"
# Kubeconfig reachable from inside the consumer cluster (server = provider node's
# IP on the docker "kind" network, not the host-mapped 127.0.0.1 port).
PROVIDER_INTERNAL_KC="$(mktemp -t kbind-e2e-provider-internal.XXXXXX)"

IMAGE_REPO="${IMAGE%:*}"
IMAGE_TAG="${IMAGE##*:}"

kp() { kubectl --kubeconfig "${PROVIDER_KC}" "$@"; }
kc() { kubectl --kubeconfig "${CONSUMER_KC}" "$@"; }

RED=$'\033[31m'; GREEN=$'\033[32m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
info() { echo "${BOLD}==>${RESET} $*"; }
pass() { echo "${GREEN}  ✓${RESET} $*"; }
fail() { echo "${RED}  ✗ $*${RESET}" >&2; exit 1; }

# retry <attempts> <delay-seconds> <cmd...> — succeeds as soon as <cmd> does.
retry() {
  local attempts=$1 delay=$2; shift 2
  local i
  for ((i = 1; i <= attempts; i++)); do
    if "$@"; then return 0; fi
    sleep "${delay}"
  done
  return 1
}

cleanup() {
  rm -f "${PROVIDER_KC}" "${CONSUMER_KC}" "${PROVIDER_INTERNAL_KC}"
  if [[ "${KEEP}" == "1" ]]; then
    cat <<EOF

${BOLD}Clusters left running (KEEP=1).${RESET} Tear down with:
  kind delete cluster --name ${PROVIDER}
  kind delete cluster --name ${CONSUMER}
EOF
    return
  fi
  info "Tearing down kind clusters"
  kind delete cluster --name "${PROVIDER}" >/dev/null 2>&1 || true
  kind delete cluster --name "${CONSUMER}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ----------------------------------------------------------------------------
info "Creating kind clusters (${PROVIDER}, ${CONSUMER})"
kind create cluster --name "${PROVIDER}" >/dev/null 2>&1 || true
kind create cluster --name "${CONSUMER}" >/dev/null 2>&1 || true
kind get kubeconfig --name "${PROVIDER}" > "${PROVIDER_KC}"
kind get kubeconfig --name "${CONSUMER}" > "${CONSUMER_KC}"

# A kind cluster can report "created" before its API server is reachable, and a
# memory-starved docker host can leave a node NotReady. Gate on real readiness
# so later steps fail loudly here instead of with a cryptic "connection refused".
info "Waiting for both clusters' API servers and nodes to be Ready"
retry "${TIMEOUT}" 2 kp get --raw=/readyz >/dev/null 2>&1 \
  || fail "provider API server never became ready (check 'docker ps' — the kind node may have been OOM-killed)"
retry "${TIMEOUT}" 2 kc get --raw=/readyz >/dev/null 2>&1 \
  || fail "consumer API server never became ready (check 'docker ps' — the kind node may have been OOM-killed)"
kp wait --for=condition=Ready nodes --all --timeout="${TIMEOUT}s" >/dev/null
kc wait --for=condition=Ready nodes --all --timeout="${TIMEOUT}s" >/dev/null
pass "both clusters are reachable and Ready"

# The konnector runs as a pod in the consumer cluster, so it cannot reach the
# provider via the host-mapped 127.0.0.1 port. Rewrite the server to the
# provider node's IP on the shared docker "kind" network; the kind API server
# cert includes that internal IP as a SAN, so TLS still verifies.
PROVIDER_IP="$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "${PROVIDER}-control-plane")"
[[ -n "${PROVIDER_IP}" ]] || fail "could not determine provider node IP on the kind network"
sed -E "s#server: https://127\.0\.0\.1:[0-9]+#server: https://${PROVIDER_IP}:6443#" \
  "${PROVIDER_KC}" > "${PROVIDER_INTERNAL_KC}"

# ----------------------------------------------------------------------------
if [[ "${NO_BUILD}" == "1" ]]; then
  info "Skipping image build (NO_BUILD=1); loading ${IMAGE} into ${CONSUMER}"
else
  info "Building konnector image ${IMAGE}"
  docker build -t "${IMAGE}" "${ROOT}"
fi
info "Loading ${IMAGE} into the consumer cluster"
kind load docker-image "${IMAGE}" --name "${CONSUMER}"

# ----------------------------------------------------------------------------
info "Provider: installing the exported Widget CRD"
kp apply -f "${SAMPLES}/provider-widget-crd.yaml"

# ----------------------------------------------------------------------------
info "Consumer: helm install the konnector (chart bundles the core CRDs)"
kc create namespace "${NAMESPACE}" --dry-run=client -o yaml | kc apply -f -
helm --kubeconfig "${CONSUMER_KC}" upgrade --install "${RELEASE}" "${CHART}" \
  --namespace "${NAMESPACE}" \
  --set image.repository="${IMAGE_REPO}" \
  --set image.tag="${IMAGE_TAG}" \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout "${TIMEOUT}s"

DEPLOY="$(kc -n "${NAMESPACE}" get deploy -l app.kubernetes.io/instance="${RELEASE}" -o name | head -n1)"
[[ -n "${DEPLOY}" ]] || fail "konnector deployment not found"
kc -n "${NAMESPACE}" rollout status "${DEPLOY}" --timeout="${TIMEOUT}s"
pass "konnector deployment is available"

# ----------------------------------------------------------------------------
info "Consumer: store the provider kubeconfig and apply the bundle"
kc -n "${NAMESPACE}" delete secret demo-provider-kubeconfig --ignore-not-found >/dev/null
kc -n "${NAMESPACE}" create secret generic demo-provider-kubeconfig \
  --from-file=kubeconfig="${PROVIDER_INTERNAL_KC}"
kc apply -f "${SAMPLES}/binding.yaml"

# ----------------------------------------------------------------------------
info "Asserting the bind flow"
kc wait --for=condition=Ready connection/demo-provider --timeout="${TIMEOUT}s" \
  || fail "Connection did not become Ready"
pass "Connection demo-provider is Ready"

retry "${TIMEOUT}" 1 kc get crd widgets.example.org >/dev/null 2>&1 \
  || fail "Widget CRD was not pulled onto the consumer"
kc wait --for=condition=Established crd/widgets.example.org --timeout="${TIMEOUT}s" >/dev/null
pass "Widget CRD pulled onto the consumer and Established"

kc wait --for=condition=Synced clusterbinding/widgets --timeout="${TIMEOUT}s" \
  || fail "ClusterBinding did not become Synced"
pass "ClusterBinding widgets is Synced"

# ----------------------------------------------------------------------------
info "Asserting spec sync UP (consumer -> provider)"
kc apply -f "${SAMPLES}/widget.yaml"
retry "${TIMEOUT}" 1 kp -n default get widget my-widget >/dev/null 2>&1 \
  || fail "Widget did not sync up to the provider"
SIZE="$(kp -n default get widget my-widget -o jsonpath='{.spec.size}')"
[[ "${SIZE}" == "large" ]] || fail "Widget synced up but spec.size=${SIZE:-<empty>} (want large)"
pass "Widget synced up to the provider (spec.size=large)"

# ----------------------------------------------------------------------------
info "Asserting status sync DOWN (provider -> consumer)"
kp -n default patch widget my-widget --subresource=status --type=merge \
  -p '{"status":{"phase":"Running"}}'
phase_is_running() {
  [[ "$(kc -n default get widget my-widget -o jsonpath='{.status.phase}' 2>/dev/null)" == "Running" ]]
}
retry "${TIMEOUT}" 1 phase_is_running \
  || fail "provider status did not flow down to the consumer"
pass "status flowed down to the consumer (status.phase=Running)"

echo
echo "${GREEN}${BOLD}E2E PASSED${RESET}"
