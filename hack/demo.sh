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

# Stands up two kind clusters (provider + consumer) and wires the v2 slim-core
# demo: a provider exports the Widget CRD, the consumer binds it, and the
# konnector syncs instances. Run the konnector separately (printed at the end).
set -euo pipefail

PROVIDER=kbind-provider
CONSUMER=kbind-consumer
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
V2="$(cd "${HERE}/.." && pwd)"
SAMPLES="${V2}/config/samples"
PROVIDER_KC=/tmp/${PROVIDER}.kubeconfig
CONSUMER_KC=/tmp/${CONSUMER}.kubeconfig

echo "==> Creating kind clusters"
kind create cluster --name "${PROVIDER}" || true
kind create cluster --name "${CONSUMER}" || true

# Internal kubeconfig so the konnector (running on the host) can reach the
# provider API server via its host-mapped port.
kind get kubeconfig --name "${PROVIDER}" > "${PROVIDER_KC}"
kind get kubeconfig --name "${CONSUMER}" > "${CONSUMER_KC}"

kp() { kubectl --kubeconfig "${PROVIDER_KC}" "$@"; }
kc() { kubectl --kubeconfig "${CONSUMER_KC}" "$@"; }

echo "==> Provider: install + export the Widget CRD"
kp apply -f "${SAMPLES}/provider-widget-crd.yaml"

echo "==> Consumer: install core CRDs and the kbind namespace"
kc apply -f "${V2}/sdk/config/crd"
kc create namespace kbind --dry-run=client -o yaml | kc apply -f -

echo "==> Consumer: store the provider kubeconfig as a Secret"
kc -n kbind delete secret demo-provider-kubeconfig --ignore-not-found
kc -n kbind create secret generic demo-provider-kubeconfig \
  --from-file=kubeconfig="${PROVIDER_KC}"

echo "==> Consumer: the one-apply bundle (Connection + ClusterBinding)"
kc apply -f "${SAMPLES}/binding.yaml"

cat <<EOF

================================================================================
Demo wired. Now run the konnector against the consumer cluster:

  cd ${V2}
  KUBECONFIG=${CONSUMER_KC} go run ./cmd/konnector

Then, in another terminal, watch it work:

  # Connection becomes Ready and lists the exported API:
  kubectl --kubeconfig ${CONSUMER_KC} get connection demo-provider -o yaml

  # The Widget CRD was pulled onto the consumer; the binding is Ready:
  kubectl --kubeconfig ${CONSUMER_KC} get crd widgets.example.org
  kubectl --kubeconfig ${CONSUMER_KC} get clusterbinding widgets

  # Create a Widget on the CONSUMER; it syncs UP to the provider:
  kubectl --kubeconfig ${CONSUMER_KC} apply -f ${SAMPLES}/widget.yaml
  kubectl --kubeconfig ${PROVIDER_KC} -n default get widgets

  # Set status on the PROVIDER copy; it flows DOWN to the consumer:
  kubectl --kubeconfig ${PROVIDER_KC} -n default patch widget my-widget \\
    --subresource=status --type=merge -p '{"status":{"phase":"Running"}}'
  kubectl --kubeconfig ${CONSUMER_KC} -n default get widget my-widget -o jsonpath='{.status.phase}{"\n"}'

Tear down:  kind delete cluster --name ${PROVIDER} && kind delete cluster --name ${CONSUMER}
================================================================================
EOF
