/*
Copyright 2026 The kbind Authors.

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
	"net/http"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kbind/kbind/backend/gateway/api"
	catalogv1alpha1 "github.com/kbind/kbind/sdk/apis/catalog/v1alpha1"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

// handleCatalog lists the Exports and Collections visible to the caller.
// Exports that are not Ready (listing APIs that are not actually exported)
// are hidden — the export label stays the source of truth, the catalog is
// presentation.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if _, err := s.identity(r); err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var exports catalogv1alpha1.ExportList
	if err := s.client.List(r.Context(), &exports); err != nil {
		writeError(w, http.StatusBadGateway, "listing catalog: "+err.Error())
		return
	}
	var collections catalogv1alpha1.CollectionList
	if err := s.client.List(r.Context(), &collections); err != nil {
		writeError(w, http.StatusBadGateway, "listing collections: "+err.Error())
		return
	}

	catalog := api.Catalog{Exports: []api.Export{}}
	visible := map[string]bool{}
	for i := range exports.Items {
		e := &exports.Items[i]
		if !apimeta.IsStatusConditionPresentAndEqual(e.Status.Conditions, catalogv1alpha1.ConditionReady, metav1.ConditionTrue) {
			continue
		}
		apis := make([]string, 0, len(e.Spec.APIs))
		for _, a := range e.Spec.APIs {
			apis = append(apis, a.Name)
		}
		out := api.Export{
			Name:        e.Name,
			Title:       e.Spec.Title,
			Description: e.Spec.Description,
			Docs:        e.Spec.Docs,
			APIs:        apis,
		}
		if e.Spec.Icon != nil {
			out.IconURL = e.Spec.Icon.URL
		}
		for _, rr := range e.Spec.Defaults.RelatedResources {
			out.RelatedResources = append(out.RelatedResources, api.RelatedResource{
				Resource:  rr.Resource,
				Direction: string(rr.Direction),
				Selector:  selectorSummary(rr),
			})
		}
		catalog.Exports = append(catalog.Exports, out)
		visible[e.Name] = true
	}

	for i := range collections.Items {
		c := &collections.Items[i]
		members := make([]string, 0, len(c.Spec.Exports))
		for _, ref := range c.Spec.Exports {
			if visible[ref.Name] {
				members = append(members, ref.Name)
			}
		}
		if len(members) == 0 {
			continue
		}
		catalog.Collections = append(catalog.Collections, api.Collection{
			Name:        c.Name,
			Title:       c.Spec.Title,
			Description: c.Spec.Description,
			Exports:     members,
		})
	}

	writeJSON(w, http.StatusOK, catalog)
}

// selectorSummary renders a related-resource selector for humans: label
// selector and/or names, empty when everything in scope is selected.
func selectorSummary(rr corev1alpha1.RelatedResource) string {
	if rr.Selector == nil {
		return ""
	}
	var parts []string
	if rr.Selector.LabelSelector != nil {
		if s := metav1.FormatLabelSelector(rr.Selector.LabelSelector); s != "" && s != "<none>" {
			parts = append(parts, s)
		}
	}
	if len(rr.Selector.Names) > 0 {
		parts = append(parts, "names: "+strings.Join(rr.Selector.Names, ", "))
	}
	return strings.Join(parts, "; ")
}
