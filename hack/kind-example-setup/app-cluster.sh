#!/usr/bin/env bash

# Copyright 2025 The Kube Bind Authors.
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

set -o nounset
set -o pipefail

DEFAULT_KONNECTOR_IMAGE="ghcr.io/kbind-dev/konnector:v0.4.6"

if [[ -z "${HOST_IP:-}" ]]; then
  source "$(dirname "$0")/host-ip.sh"
  get_host_ip
fi

cat << EOF_AppClusterDefinition | kind create cluster --config=-
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
name: app
networking:
  apiServerAddress: ${HOST_IP}
EOF_AppClusterDefinition

kubectl config use-context kind-app

if [[ -n "${KONNECTOR_IMAGE:-}" ]]; then
  pushd "$(dirname "$0")/../.."
  KIND_CLUSTER=app make kind-load
  popd

  if [[ -z "${TAG:-}" ]]; then
    REV=$(git rev-parse --short HEAD)
    TAG=${REV}
  fi
  konnector_image="${KONNECTOR_IMAGE}:${TAG}"
  echo "Using override konnector image: ${konnector_image}"
  $(dirname "$0")/../../bin/kubectl-bind --konnector-image=${konnector_image} http://${HOST_IP}:8080/export
else
  konnector_image=${DEFAULT_KONNECTOR_IMAGE}
  echo "Using default konnector image: ${konnector_image}"
  kubectl bind --konnector-image=${konnector_image} http://${HOST_IP}:8080/export
fi
