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

// Package kubeapply is a small server-side-apply helper for applying YAML
// multi-docs (bundles, konnector manifests) to a cluster, used by the CLI and
// the gateway's browser-apply path. It tolerates CRDs and their instances in
// one batch: unresolvable kinds are retried while the just-applied CRDs
// establish.
package kubeapply

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

// DecodeYAML splits a YAML multi-doc into unstructured objects, skipping
// empty documents.
func DecodeYAML(data []byte) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				return objs, nil
			}
			return nil, fmt.Errorf("decoding manifest: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		objs = append(objs, &obj)
	}
}

// NamespaceObject returns a minimal Namespace as unstructured — used to make
// sure the konnector namespace exists before a bundle's Secret lands in it.
func NamespaceObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}}
}

// Applier server-side-applies unstructured objects.
type Applier struct {
	dyn    dynamic.Interface
	mapper *restmapper.DeferredDiscoveryRESTMapper
	owner  string
}

// New builds an Applier for the cluster behind cfg.
func New(cfg *rest.Config, fieldOwner string) (*Applier, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Applier{
		dyn:    dyn,
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disco)),
		owner:  fieldOwner,
	}, nil
}

// Apply server-side-applies the objects in order (force-conflicts, so a
// re-apply of a previous bundle wins over drift). Kinds not yet resolvable —
// instances of CRDs applied moments ago — are retried until the mapping
// appears or the timeout passes. Returns "Kind/name" for everything applied.
func (a *Applier) Apply(ctx context.Context, objs []*unstructured.Unstructured) ([]string, error) {
	applied := make([]string, 0, len(objs))
	for _, obj := range objs {
		if err := a.applyOne(ctx, obj); err != nil {
			return applied, fmt.Errorf("applying %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		applied = append(applied, fmt.Sprintf("%s/%s", obj.GetKind(), obj.GetName()))
	}
	return applied, nil
}

func (a *Applier) applyOne(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	deadline := time.Now().Add(30 * time.Second)
	for {
		mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err == nil {
			return a.patch(ctx, mapping, obj)
		}
		if !meta.IsNoMatchError(err) || time.Now().After(deadline) {
			return err
		}
		// Likely a CRD applied a moment ago that is still establishing.
		a.mapper.Reset()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (a *Applier) patch(ctx context.Context, mapping *meta.RESTMapping, obj *unstructured.Unstructured) error {
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return err
	}
	var res dynamic.ResourceInterface = a.dyn.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		res = a.dyn.Resource(mapping.Resource).Namespace(obj.GetNamespace())
	}
	force := true
	_, err = res.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: a.owner,
		Force:        &force,
	})
	return err
}
