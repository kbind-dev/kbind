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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

// cleanup unwinds what a binding created when it is deleted. For each API the
// binding bound that no *other* binding still covers, it deletes the provider
// copies of synced instances (best effort), releases the syncer finalizer on
// the consumer instances, and — for a cluster-wide unbind — removes the pulled
// CRD (which cascade-deletes the now finalizer-free instances).
//
//   - namespace: "" for a cluster-wide unbind; otherwise the Binding's namespace.
//   - removeCRD: true for ClusterBinding (owns the CRD), false for Binding.
func (b *base) cleanup(ctx context.Context, obj corev1alpha1.BindingAccessor, namespace string, removeCRD bool) error {
	log := ctrl.LoggerFrom(ctx)
	spec := obj.BindingSpecP()

	// Best-effort provider client: the Secret/Connection may already be gone
	// during a `delete -f`. If unavailable, we still release finalizers so the
	// consumer never wedges; orphaned provider copies are the reaper's job.
	var providerClient client.Client
	conn := &corev1alpha1.Connection{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: spec.ConnectionRef.Name}, conn); err == nil {
		if pc, perr := b.providerClient(ctx, conn); perr == nil {
			providerClient = pc
		} else {
			log.V(2).Info("provider client unavailable during cleanup; releasing finalizers only", "err", perr.Error())
		}
	}
	localUID := conn.Status.LocalClusterUID

	for _, api := range spec.APIs {
		if b.otherBindingCovers(ctx, api.Name, obj) {
			continue
		}
		gvr, ok, err := b.gvrFor(ctx, api.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue // CRD already gone
		}

		if err := b.drainInstances(ctx, gvr, namespace, providerClient, localUID); err != nil {
			return err
		}

		if removeCRD {
			crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: api.Name}}
			if err := b.client.Delete(ctx, crd); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("deleting pulled CRD %s: %w", api.Name, err)
			}
		}
	}

	// Delete related Secret/ConfigMap copies this binding created.
	return b.cleanupRelated(ctx, obj, namespace, providerClient)
}

// countConflicts counts consumer instances of gvr in scope that the syncer
// marked as conflicting (the conflict annotation).
func (b *base) countConflicts(ctx context.Context, gvr schema.GroupVersionResource, namespace string) int32 {
	ri := b.dyn.Resource(gvr)
	var lister dynamicLister = ri
	if namespace != "" {
		lister = ri.Namespace(namespace)
	}
	list, err := lister.List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0
	}
	var n int32
	for i := range list.Items {
		if list.Items[i].GetAnnotations()[corev1alpha1.AnnotationConflict] != "" {
			n++
		}
	}
	return n
}

// drainInstances deletes provider copies and releases the syncer finalizer on
// every consumer instance of gvr in scope.
func (b *base) drainInstances(ctx context.Context, gvr schema.GroupVersionResource, namespace string, providerClient client.Client, localUID string) error {
	ri := b.dyn.Resource(gvr)
	var lister dynamicLister = ri
	if namespace != "" {
		lister = ri.Namespace(namespace)
	}
	list, err := lister.List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing instances of %s: %w", gvr.Resource, err)
	}

	for i := range list.Items {
		inst := &list.Items[i]
		if !controllerutil.ContainsFinalizer(inst, corev1alpha1.FinalizerSyncer) {
			continue
		}
		// Delete the provider copy if it is ours (best effort), unless the
		// instance opted out with deletion-policy: Orphan.
		if providerClient != nil && inst.GetAnnotations()[corev1alpha1.AnnotationDeletionPolicy] != corev1alpha1.DeletionPolicyOrphan {
			if err := deleteProviderCopy(ctx, providerClient, gvr, inst, localUID); err != nil {
				return err
			}
		}
		// Release the syncer finalizer so the consumer instance can be deleted.
		controllerutil.RemoveFinalizer(inst, corev1alpha1.FinalizerSyncer)
		var updErr error
		if inst.GetNamespace() == "" {
			_, updErr = ri.Update(ctx, inst, metav1.UpdateOptions{})
		} else {
			_, updErr = ri.Namespace(inst.GetNamespace()).Update(ctx, inst, metav1.UpdateOptions{})
		}
		if client.IgnoreNotFound(updErr) != nil {
			return fmt.Errorf("releasing finalizer on %s/%s: %w", inst.GetNamespace(), inst.GetName(), updErr)
		}
	}
	return nil
}

func deleteProviderCopy(ctx context.Context, providerClient client.Client, gvr schema.GroupVersionResource, inst *unstructured.Unstructured, localUID string) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvkFromGVR(gvr, inst))
	err := providerClient.Get(ctx, client.ObjectKey{Namespace: inst.GetNamespace(), Name: inst.GetName()}, existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		// Best effort: if we can't read the provider object during teardown we
		// can't confirm ownership, so skip rather than fail the whole unbind.
		return nil //nolint:nilerr // intentional best-effort skip
	}
	// Only delete if it carries our markers.
	ann := existing.GetAnnotations()
	if ann[corev1alpha1.AnnotationConsumerClusterUID] != localUID || ann[corev1alpha1.AnnotationConsumerObjectUID] != string(inst.GetUID()) {
		return nil
	}
	return client.IgnoreNotFound(providerClient.Delete(ctx, existing))
}

func gvkFromGVR(gvr schema.GroupVersionResource, inst *unstructured.Unstructured) schema.GroupVersionKind {
	if gvk := inst.GroupVersionKind(); gvk.Kind != "" {
		return gvk
	}
	return schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version}
}

// otherBindingCovers reports whether a binding other than self lists crdName.
func (b *base) otherBindingCovers(ctx context.Context, crdName string, self corev1alpha1.BindingAccessor) bool {
	var cbs corev1alpha1.ClusterBindingList
	if err := b.client.List(ctx, &cbs); err == nil {
		for i := range cbs.Items {
			if cbs.Items[i].DeletionTimestamp != nil || cbs.Items[i].UID == self.GetUID() {
				continue
			}
			if listsAPI(cbs.Items[i].Spec.APIs, crdName) {
				return true
			}
		}
	}
	var bs corev1alpha1.BindingList
	if err := b.client.List(ctx, &bs); err == nil {
		for i := range bs.Items {
			if bs.Items[i].DeletionTimestamp != nil || bs.Items[i].UID == self.GetUID() {
				continue
			}
			if listsAPI(bs.Items[i].Spec.APIs, crdName) {
				return true
			}
		}
	}
	return false
}

// gvrFor resolves the GVR for an exported API CRD name from the pulled consumer
// CRD (storage/served version). Returns ok=false if the CRD is already gone.
func (b *base) gvrFor(ctx context.Context, crdName string) (schema.GroupVersionResource, bool, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return schema.GroupVersionResource{}, false, nil
		}
		return schema.GroupVersionResource{}, false, err
	}
	version := ""
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			version = v.Name
			break
		}
		if version == "" && v.Served {
			version = v.Name
		}
	}
	if version == "" {
		return schema.GroupVersionResource{}, false, nil
	}
	return schema.GroupVersionResource{Group: crd.Spec.Group, Version: version, Resource: crd.Spec.Names.Plural}, true, nil
}

func listsAPI(apis []corev1alpha1.APIRef, crdName string) bool {
	for _, a := range apis {
		if a.Name == crdName {
			return true
		}
	}
	return false
}

// dynamicLister is the common List surface of a (namespaced or not) dynamic
// resource interface.
type dynamicLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
}
