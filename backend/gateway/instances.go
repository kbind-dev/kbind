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
	"context"
	"net/http"
	"sort"

	coordinationv1 "k8s.io/api/coordination/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kbind/kbind/backend/gateway/api"
	catalogv1alpha1 "github.com/kbind/kbind/sdk/apis/catalog/v1alpha1"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// handleExportInstances lists the provider-side objects consumers synced
// under one catalog item — the catalog's "what is actually running against
// this offering" view. Objects are attributed by the sync engine's ownership
// markers; identities are resolved through the heartbeat Lease → Grant link
// where one exists.
func (s *Server) handleExportInstances(w http.ResponseWriter, r *http.Request) {
	if _, err := s.identity(r); err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	ctx := r.Context()
	name := r.PathValue("export")

	export := &catalogv1alpha1.Export{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: name}, export); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "unknown export "+name)
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	identityByCluster, err := s.identitiesForExport(ctx, name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "resolving identities: "+err.Error())
		return
	}

	out := api.ExportInstances{Export: name, Instances: []api.Instance{}}
	for _, ref := range export.Spec.APIs {
		items, err := s.listManaged(ctx, ref.Name)
		if err != nil {
			// The API may not be established (or readable) yet; the view is
			// best-effort per API, like the clusters view's counts.
			continue
		}
		for i := range items {
			item := &items[i]
			uid := item.GetAnnotations()[corev1alpha1.AnnotationConsumerClusterUID]
			out.Instances = append(out.Instances, api.Instance{
				API:        ref.Name,
				Namespace:  item.GetNamespace(),
				Name:       item.GetName(),
				ClusterUID: uid,
				Identity:   identityByCluster[uid],
				CreatedAt:  item.GetCreationTimestamp().Time.UTC(),
			})
		}
	}
	// Related resources flow alongside the APIs — show them too.
	// FromConsumer copies exist here with sync markers; FromProvider rows
	// are the provider originals matching the rule's selector.
	for _, rr := range export.Spec.Defaults.RelatedResources {
		items, err := s.listRelated(ctx, rr)
		if err != nil {
			continue // best-effort, like the bound-API listings
		}
		for i := range items {
			item := &items[i]
			uid := item.GetAnnotations()[corev1alpha1.AnnotationConsumerClusterUID]
			out.Instances = append(out.Instances, api.Instance{
				API:        rr.Resource,
				Related:    true,
				Direction:  string(rr.Direction),
				Namespace:  item.GetNamespace(),
				Name:       item.GetName(),
				ClusterUID: uid,
				Identity:   identityByCluster[uid],
				CreatedAt:  item.GetCreationTimestamp().Time.UTC(),
			})
		}
	}

	sort.Slice(out.Instances, func(a, b int) bool {
		x, y := out.Instances[a], out.Instances[b]
		if x.Related != y.Related {
			return !x.Related // bound-API instances first
		}
		if x.API != y.API {
			return x.API < y.API
		}
		if x.Namespace != y.Namespace {
			return x.Namespace < y.Namespace
		}
		return x.Name < y.Name
	})
	writeJSON(w, http.StatusOK, out)
}

// listRelated lists one related-resource rule's objects on the provider.
// FromConsumer: the managed copies consumers synced here. FromProvider: the
// unmanaged originals matching the rule's selector (label selector required;
// names-only rules additionally filter by name).
func (s *Server) listRelated(ctx context.Context, rr corev1alpha1.RelatedResource) ([]unstructured.Unstructured, error) {
	kind := "Secret"
	if rr.Resource == "configmaps" {
		kind = "ConfigMap"
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: kind + "List"})

	var opts []client.ListOption
	if rr.Direction == corev1alpha1.FromConsumer {
		opts = append(opts, client.MatchingLabels{corev1alpha1.LabelManaged: "true"})
	} else {
		// Never list every secret on the provider: require a label selector
		// for the originals view.
		if rr.Selector == nil || rr.Selector.LabelSelector == nil {
			return nil, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(rr.Selector.LabelSelector)
		if err != nil {
			return nil, err
		}
		opts = append(opts, client.MatchingLabelsSelector{Selector: selector})
	}
	if err := s.client.List(ctx, list, opts...); err != nil {
		return nil, err
	}

	names := map[string]bool{}
	if rr.Selector != nil {
		for _, n := range rr.Selector.Names {
			names[n] = true
		}
	}
	var out []unstructured.Unstructured
	for i := range list.Items {
		item := list.Items[i]
		if len(names) > 0 && !names[item.GetName()] {
			continue
		}
		if rr.Direction == corev1alpha1.FromConsumer {
			// Only copies this layer's sync wrote, not issuer artifacts etc.
			if item.GetAnnotations()[corev1alpha1.AnnotationRelatedBinding] == "" {
				continue
			}
		} else if item.GetLabels()[corev1alpha1.LabelManaged] == "true" {
			continue // a FromConsumer copy, not an original
		}
		out = append(out, item)
	}
	return out, nil
}

// listManaged lists the provider objects of one bound API that the sync
// engine wrote (managed label).
func (s *Server) listManaged(ctx context.Context, apiName string) ([]unstructured.Unstructured, error) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := s.client.Get(ctx, types.NamespacedName{Name: apiName}, &crd); err != nil {
		return nil, err
	}
	version := ""
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			version = v.Name
			break
		}
		if v.Served && version == "" {
			version = v.Name
		}
	}
	if version == "" {
		return nil, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   crd.Spec.Group,
		Version: version,
		Kind:    crd.Spec.Names.Kind + "List",
	})
	if err := s.client.List(ctx, list, client.MatchingLabels{corev1alpha1.LabelManaged: "true"}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// identitiesForExport maps consumer cluster UIDs to the identity behind the
// export's grants, via the heartbeat Lease → Grant (connection name) link.
func (s *Server) identitiesForExport(ctx context.Context, exportName string) (map[string]string, error) {
	var grants iamv1alpha1.GrantList
	if err := s.client.List(ctx, &grants); err != nil {
		return nil, err
	}
	identities := map[string]string{}
	for i := range grants.Items {
		grant := &grants.Items[i]
		if grant.Spec.ExportName != exportName || grant.Status.Namespace == "" {
			continue
		}
		var leases coordinationv1.LeaseList
		if err := s.client.List(ctx, &leases, client.InNamespace(grant.Status.Namespace),
			client.MatchingLabels{corev1alpha1.LabelManaged: "true"}); err != nil {
			continue
		}
		for j := range leases.Items {
			lease := &leases.Items[j]
			if lease.Annotations[corev1alpha1.AnnotationConnection] != grant.Name {
				continue
			}
			if uid := lease.Annotations[corev1alpha1.AnnotationConsumerClusterUID]; uid != "" {
				identities[uid] = grant.Spec.Identity.DisplayName
			}
		}
	}
	return identities, nil
}
