/*
Copyright 2026 The Kbind Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kbind/kbind/backend/gateway/api"
	"github.com/kbind/kbind/backend/issuer"
	"github.com/kbind/kbind/pkg/konnectorinstall"
	"github.com/kbind/kbind/pkg/kubeapply"
)

// handleApply is the flag-gated browser-apply path: bind + apply the bundle
// (and optionally install the konnector) into a consumer cluster using a
// caller-supplied kubeconfig. Enabling it means consumer credentials transit
// the gateway — a deployment-level decision, off by default.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req api.ApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Kubeconfig == "" {
		writeError(w, http.StatusBadRequest, "body must be {\"export\": ..., \"kubeconfig\": <base64>}")
		return
	}
	if req.Export == "" && !req.InstallKonnector {
		writeError(w, http.StatusBadRequest, "nothing to do: set export and/or installKonnector")
		return
	}
	kubeconfig, err := base64.StdEncoding.DecodeString(req.Kubeconfig)
	if err != nil {
		writeError(w, http.StatusBadRequest, "kubeconfig must be base64-encoded")
		return
	}
	consumerCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parsing consumer kubeconfig: "+err.Error())
		return
	}

	var objs []*unstructured.Unstructured
	if req.InstallKonnector {
		konnectorObjs, err := konnectorinstall.Objects(s.cfg.KonnectorImage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		objs = append(objs, konnectorObjs...)
	} else {
		// The bundle's Secret lands in the konnector namespace; make sure it
		// exists even when the konnector install is skipped.
		objs = append(objs, kubeapply.NamespaceObject(issuer.KonnectorNamespace))
	}

	// With an export, run the same issuance path as bind — the terminal
	// output is the same bundle. Without one this is a pure "connect a
	// cluster": konnector install only.
	if req.Export != "" {
		grant, err := s.ensureGrant(r.Context(), id, req.Export)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := s.waitGrantReady(r.Context(), grant); err != nil {
			writeError(w, http.StatusGatewayTimeout, "credentials not provisioned yet: "+err.Error())
			return
		}
		bundleKubeconfig, err := s.bundle.Kubeconfig(r.Context(), grant)
		if err != nil {
			writeError(w, http.StatusBadGateway, "assembling kubeconfig: "+err.Error())
			return
		}
		for _, obj := range s.bundle.Objects(grant, bundleKubeconfig) {
			content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			objs = append(objs, &unstructured.Unstructured{Object: content})
		}
	}

	applier, err := kubeapply.New(consumerCfg, "kbind-gateway")
	if err != nil {
		writeError(w, http.StatusBadRequest, "connecting to consumer cluster: "+err.Error())
		return
	}
	applied, err := applier.Apply(r.Context(), objs)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ApplyResponse{Applied: applied})
}
