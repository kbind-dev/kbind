#!/usr/bin/env bash

# Copyright 2026 The Kbind Authors.
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

# Create the two kind clusters for the Tilt dev loop (idempotent):
#   kbind-provider — runs the backend (gateway + issuer)
#   kbind-consumer — runs the konnector
# Both live on the shared "kind" docker network, so consumer pods reach the
# provider API server at https://kbind-provider-control-plane:6443.

set -euo pipefail

for cluster in kbind-provider kbind-consumer; do
  if ! kind get clusters 2>/dev/null | grep -qx "${cluster}"; then
    kind create cluster --name "${cluster}"
  else
    echo "kind cluster ${cluster} already exists"
  fi
done

kubectl config use-context kind-kbind-provider
echo
echo "Ready. Start the dev loop with:"
echo "  cd $(dirname "${BASH_SOURCE[0]}") && tilt up"
