/*
Copyright 2026 The Kube Bind Authors.

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

// Package openapi synthesizes CRDs for a CRD-less provider (kcp, aggregated
// APIs) from server discovery + the published /openapi/v3 schemas. This is the
// schema.source: OpenAPI path. Known fidelity limits (CEL, defaulting,
// multi-version conversion) are accepted — the provider remains the enforcing
// side.
package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// builtinGroups are never exported via the OpenAPI boundary.
var builtinGroups = map[string]bool{
	"":                  true, // core
	"apps":              true,
	"batch":             true,
	"extensions":        true,
	"policy":            true,
	"autoscaling":       true,
	"networking.k8s.io": true,
}

// isBuiltinGroup reports whether a group is a Kubernetes built-in (excluded from
// the OpenAPI export boundary).
func isBuiltinGroup(group string) bool {
	return builtinGroups[group] || strings.HasSuffix(group, ".k8s.io") || group == "apiregistration.k8s.io"
}

// crdVersionInfo holds the per-version data collected during discovery.
type crdVersionInfo struct {
	gv        schema.GroupVersion
	resource  metav1.APIResource
	hasStatus bool
	props     *apiextensionsv1.JSONSchemaProps
}

// SynthesizeCRDs discovers every served, non-built-in API on the provider and
// synthesizes a consumer-installable CRD for each from its /openapi/v3 schema.
// All discovered versions of a group-resource are merged into a single CRD;
// the preferred version (per discovery) is marked as the storage version.
func SynthesizeCRDs(ctx context.Context, cfg *rest.Config) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	apiGroups, resourceLists, err := dc.ServerGroupsAndResources()
	if err != nil {
		// Partial discovery (an aggregated API may be down) is tolerable.
		if len(resourceLists) == 0 {
			return nil, fmt.Errorf("discovery: %w", err)
		}
	}

	// Build preferred-version map (group → preferred version name) from discovery.
	preferredVersion := make(map[string]string, len(apiGroups))
	for _, g := range apiGroups {
		preferredVersion[g.Name] = g.PreferredVersion.Version
	}

	root := dc.OpenAPIV3()
	paths, err := root.Paths()
	if err != nil {
		return nil, fmt.Errorf("openapi v3 paths: %w", err)
	}

	// Collect all versions per group-resource before building CRDs.
	versionsForGR := map[schema.GroupResource][]crdVersionInfo{}
	for _, rl := range resourceLists {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil || isBuiltinGroup(gv.Group) {
			continue
		}

		hasStatus := map[string]bool{}
		for _, r := range rl.APIResources {
			if base, sub, ok := strings.Cut(r.Name, "/"); ok && sub == "status" {
				hasStatus[base] = true
			}
		}

		gvObj, ok := paths["apis/"+gv.Group+"/"+gv.Version]
		if !ok {
			continue
		}
		doc, err := gvObj.Schema("application/json")
		if err != nil {
			continue
		}

		for _, r := range rl.APIResources {
			if strings.Contains(r.Name, "/") { // skip subresources
				continue
			}
			gvk := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: r.Kind}
			props, err := schemaForGVK(doc, gvk)
			if err != nil {
				continue // no usable schema; skip this resource
			}
			gr := schema.GroupResource{Group: gv.Group, Resource: r.Name}
			versionsForGR[gr] = append(versionsForGR[gr], crdVersionInfo{
				gv:        gv,
				resource:  r,
				hasStatus: hasStatus[r.Name],
				props:     props,
			})
		}
	}

	out := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(versionsForGR))
	for gr, versions := range versionsForGR {
		out = append(out, buildCRD(gr, versions, preferredVersion[gr.Group]))
	}
	return out, nil
}

func buildCRD(gr schema.GroupResource, versions []crdVersionInfo, preferredVer string) *apiextensionsv1.CustomResourceDefinition {
	// Keep deterministic order so that we don't trigger updates without actually changing the schema.
	sort.SliceStable(versions, func(i, j int) bool {
		return k8sversion.CompareKubeAwareVersionStrings(versions[i].gv.Version, versions[j].gv.Version) > 0
	})

	// Determine the storage version: use the preferred version if it was
	// discovered, otherwise fall back to the highest-priority version.
	storageVer := preferredVer
	storageFound := false
	for _, v := range versions {
		if v.gv.Version == storageVer {
			storageFound = true
			break
		}
	}
	if !storageFound {
		storageVer = versions[0].gv.Version
	}

	// Canonical resource metadata (names, scope) comes from the storage version.
	canonical := versions[0]
	for _, v := range versions {
		if v.gv.Version == storageVer {
			canonical = v
			break
		}
	}

	scope := apiextensionsv1.ClusterScoped
	if canonical.resource.Namespaced {
		scope = apiextensionsv1.NamespaceScoped
	}
	singular := canonical.resource.SingularName
	if singular == "" {
		singular = strings.ToLower(canonical.resource.Kind)
	}

	crdVersions := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(versions))
	for _, v := range versions {
		crdv := apiextensionsv1.CustomResourceDefinitionVersion{
			Name:    v.gv.Version,
			Served:  true,
			Storage: v.gv.Version == storageVer,
			Schema:  &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: v.props},
		}
		if v.hasStatus {
			crdv.Subresources = &apiextensionsv1.CustomResourceSubresources{
				Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
			}
		}
		crdVersions = append(crdVersions, crdv)
	}

	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: gr.Resource + "." + gr.Group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: gr.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   canonical.resource.Name,
				Singular: singular,
				Kind:     canonical.resource.Kind,
				ListKind: canonical.resource.Kind + "List",
			},
			Scope:    scope,
			Versions: crdVersions,
		},
	}
}

// schemaForGVK finds the component schema for gvk in an OpenAPI v3 document and
// converts it to a structural CRD schema (JSON round-trip + cleanup).
func schemaForGVK(doc []byte, gvk schema.GroupVersionKind) (*apiextensionsv1.JSONSchemaProps, error) {
	var parsed struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, err
	}
	for _, raw := range parsed.Components.Schemas {
		var meta struct {
			GVK []struct {
				Group   string `json:"group"`
				Version string `json:"version"`
				Kind    string `json:"kind"`
			} `json:"x-kubernetes-group-version-kind"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		for _, g := range meta.GVK {
			if g.Group == gvk.Group && g.Version == gvk.Version && g.Kind == gvk.Kind {
				var props apiextensionsv1.JSONSchemaProps
				if err := json.Unmarshal(raw, &props); err != nil {
					return nil, err
				}
				return cleanSchema(&props), nil
			}
		}
	}
	return nil, fmt.Errorf("no openapi schema for %s", gvk)
}

// cleanSchema turns a top-level object schema into a structural CRD schema: drop
// the apiserver-managed apiVersion/kind/metadata (metadata is a $ref that won't
// resolve in a CRD), and guarantee an object type with unknown fields preserved
// where a sub-schema is otherwise empty.
func cleanSchema(p *apiextensionsv1.JSONSchemaProps) *apiextensionsv1.JSONSchemaProps {
	p.Type = "object"
	p.Ref = nil
	if p.Properties != nil {
		delete(p.Properties, "apiVersion")
		delete(p.Properties, "kind")
		delete(p.Properties, "metadata")
		for k, v := range p.Properties {
			p.Properties[k] = *normalize(&v)
		}
	} else {
		preserve := true
		p.XPreserveUnknownFields = &preserve
	}
	return p
}

// normalize makes a sub-schema structural: drop $ref (unresolvable in a CRD) and
// preserve unknown fields where the type is unknown.
func normalize(p *apiextensionsv1.JSONSchemaProps) *apiextensionsv1.JSONSchemaProps {
	if p.Ref != nil {
		preserve := true
		return &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: &preserve}
	}
	if p.Type == "" && p.XPreserveUnknownFields == nil {
		preserve := true
		p.Type = "object"
		p.XPreserveUnknownFields = &preserve
	}
	for k, v := range p.Properties {
		p.Properties[k] = *normalize(&v)
	}
	if p.Items != nil && p.Items.Schema != nil {
		p.Items.Schema = normalize(p.Items.Schema)
	}
	return p
}
