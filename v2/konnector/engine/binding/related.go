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

package binding

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// relatedFieldOwner is the SSA field manager for related-resource writes.
const relatedFieldOwner = "kube-bind-konnector-related"

// relatedGVK maps a related-resource kind to its GVK. Only secrets/configmaps.
func relatedGVK(resource string) (schema.GroupVersionKind, bool) {
	switch resource {
	case "secrets":
		return schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, true
	case "configmaps":
		return schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, true
	default:
		return schema.GroupVersionKind{}, false
	}
}

// reconcileRelated syncs the binding's related Secrets/ConfigMaps in the
// declared direction, scoped like the binding, and garbage-collects copies that
// no longer match. Identity-preserving, marked with the binding UID.
func (b *base) reconcileRelated(ctx context.Context, obj corev1alpha1.BindingAccessor, namespace string, providerClient client.Client, localUID string) error {
	spec := obj.BindingSpecP()
	bindingUID := string(obj.GetUID())
	for i := range spec.RelatedResources {
		rr := &spec.RelatedResources[i]
		gvk, ok := relatedGVK(rr.Resource)
		if !ok {
			continue
		}
		source, target := b.client, providerClient // FromConsumer
		if rr.Direction == corev1alpha1.FromProvider {
			source, target = providerClient, b.client
		}

		sel := labels.Everything()
		if rr.Selector != nil && rr.Selector.LabelSelector != nil {
			s, err := metav1.LabelSelectorAsSelector(rr.Selector.LabelSelector)
			if err != nil {
				return fmt.Errorf("invalid related selector: %w", err)
			}
			sel = s
		}
		names := nameSet(rr.Selector)

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		opts := []client.ListOption{client.MatchingLabelsSelector{Selector: sel}}
		if namespace != "" {
			opts = append(opts, client.InNamespace(namespace))
		}
		if err := source.List(ctx, list, opts...); err != nil {
			return fmt.Errorf("listing related %s: %w", rr.Resource, err)
		}

		matched := map[client.ObjectKey]bool{}
		for j := range list.Items {
			src := &list.Items[j]
			if names != nil && !names[src.GetName()] {
				continue
			}
			key := client.ObjectKey{Namespace: src.GetNamespace(), Name: src.GetName()}
			matched[key] = true
			if err := applyRelated(ctx, target, gvk, src, localUID, bindingUID); err != nil {
				return err
			}
		}
		if err := b.gcRelated(ctx, target, gvk, namespace, bindingUID, matched); err != nil {
			return err
		}
	}
	return nil
}

// applyRelated copies a related object to the target side (identity-preserving)
// with our markers, refusing to overwrite a foreign object of the same name.
func applyRelated(ctx context.Context, target client.Client, gvk schema.GroupVersionKind, src *unstructured.Unstructured, localUID, bindingUID string) error {
	key := client.ObjectKey{Namespace: src.GetNamespace(), Name: src.GetName()}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	switch err := target.Get(ctx, key, existing); {
	case err == nil:
		if existing.GetAnnotations()[corev1alpha1.AnnotationRelatedBinding] != bindingUID {
			return nil // foreign object — never overwrite
		}
	case apierrors.IsNotFound(err):
		if src.GetNamespace() != "" {
			if err := ensureNamespace(ctx, target, src.GetNamespace(), localUID); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("getting related target: %w", err)
	}

	out := &unstructured.Unstructured{}
	out.SetGroupVersionKind(gvk)
	out.SetNamespace(src.GetNamespace())
	out.SetName(src.GetName())
	out.SetLabels(map[string]string{corev1alpha1.LabelManaged: "true"})
	out.SetAnnotations(map[string]string{
		corev1alpha1.AnnotationConsumerClusterUID: localUID,
		corev1alpha1.AnnotationRelatedBinding:     bindingUID,
	})
	for _, f := range []string{"data", "stringData", "binaryData", "type", "immutable"} {
		if v, ok, _ := unstructured.NestedFieldCopy(src.Object, f); ok {
			_ = unstructured.SetNestedField(out.Object, v, f)
		}
	}
	if err := target.Patch(ctx, out, client.Apply, client.FieldOwner(relatedFieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("applying related %s/%s: %w", src.GetNamespace(), src.GetName(), err)
	}
	return nil
}

// gcRelated deletes target copies we own (this binding) that are no longer in
// the matched set.
func (b *base) gcRelated(ctx context.Context, target client.Client, gvk schema.GroupVersionKind, namespace, bindingUID string, matched map[client.ObjectKey]bool) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	opts := []client.ListOption{client.MatchingLabels{corev1alpha1.LabelManaged: "true"}}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := target.List(ctx, list, opts...); err != nil {
		return fmt.Errorf("listing managed related %s: %w", gvk.Kind, err)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if o.GetAnnotations()[corev1alpha1.AnnotationRelatedBinding] != bindingUID {
			continue
		}
		if matched[client.ObjectKey{Namespace: o.GetNamespace(), Name: o.GetName()}] {
			continue
		}
		if err := target.Delete(ctx, o); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("gc related %s/%s: %w", o.GetNamespace(), o.GetName(), err)
		}
	}
	return nil
}

// cleanupRelated deletes all related copies this binding created, on unbind.
func (b *base) cleanupRelated(ctx context.Context, obj corev1alpha1.BindingAccessor, namespace string, providerClient client.Client) error {
	spec := obj.BindingSpecP()
	bindingUID := string(obj.GetUID())
	for i := range spec.RelatedResources {
		rr := &spec.RelatedResources[i]
		gvk, ok := relatedGVK(rr.Resource)
		if !ok {
			continue
		}
		target := providerClient // FromConsumer copies live on the provider
		if rr.Direction == corev1alpha1.FromProvider {
			target = b.client
		}
		if target == nil {
			continue
		}
		if err := b.gcRelated(ctx, target, gvk, namespace, bindingUID, nil); err != nil {
			return err
		}
	}
	return nil
}

func nameSet(sel *corev1alpha1.RelatedResourceSelector) map[string]bool {
	if sel == nil || len(sel.Names) == 0 {
		return nil
	}
	m := make(map[string]bool, len(sel.Names))
	for _, n := range sel.Names {
		m[n] = true
	}
	return m
}

// ensureNamespace creates the target namespace if absent (best-effort markers).
func ensureNamespace(ctx context.Context, c client.Client, name, localUID string) error {
	ns := &unstructured.Unstructured{}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
	ns.SetName(name)
	ns.SetLabels(map[string]string{corev1alpha1.LabelManaged: "true"})
	ns.SetAnnotations(map[string]string{corev1alpha1.AnnotationConsumerClusterUID: localUID})
	if err := c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
