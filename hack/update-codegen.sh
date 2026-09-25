#!/usr/bin/env bash

# Copyright 2021 The Kube Bind Authors.
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

set -o errexit
set -o nounset
set -o pipefail

cd "$(dirname "$0")/.."

CONTROLLER_GEN="$(UGET_PRINT_PATH=absolute make --no-print-directory install-controller-gen)"
YAML_PATCH="$(UGET_PRINT_PATH=absolute make --no-print-directory install-yaml-patch)"

# Update generated CRD YAML
(
   cd sdk/apis
   "$CONTROLLER_GEN" \
      crd \
      rbac:roleName=manager-role \
      webhook \
      paths="./..." \
      output:crd:artifacts:config=../../deploy/crd

   "$CONTROLLER_GEN" \
      crd \
      rbac:roleName=manager-role \
      webhook \
      paths="./..." \
      output:crd:artifacts:config=../../deploy/charts/backend/templates/crds

   # Wrap generated CRDs in helm conditional
   for CRD in ../../deploy/charts/backend/templates/crds/*.yaml; do
      if [ -f "$CRD" ]; then
         echo '{{- if .Values.crds.install }}' > "${CRD}.tmp"
         cat "${CRD}" >> "${CRD}.tmp"
         echo '{{- end }}' >> "${CRD}.tmp"
         mv "${CRD}.tmp" "${CRD}"
      fi
   done

   # Generate RBAC manifests for Helm chart from backend controllers
   "$CONTROLLER_GEN" \
      rbac:roleName=kube-bind-role \
      paths="../../backend/controllers/..." \
      output:rbac:artifacts:config=../../deploy/charts/backend/templates
)

(
   cd deploy/crd
   for CRD in *.yaml; do
      if [ -f "../patches/${CRD}-patch" ]; then
         echo "Applying ${CRD}-patch"
         "$YAML_PATCH" -o "../patches/${CRD}-patch" < "${CRD}" > "${CRD}.patched"
         mv "${CRD}.patched" "${CRD}"
      fi
   done
)

(
   make -C contrib/kcp codegen
)
