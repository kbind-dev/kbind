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

package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kube-bind/kube-bind/v2/konnector/engine/remote"
	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

const (
	// fieldOwner is the server-side-apply field manager for spec writes.
	fieldOwner = "kube-bind-konnector"
	// statusResyncInterval re-checks provider status for drift. POC uses
	// polling; an event-driven provider watch is the follow-up (proposal #2).
	statusResyncInterval = 30 * time.Second
)

// providerCache builds and caches a direct provider client per Connection.
//
// TODO(v2): use the multicluster-runtime engaged cluster's client/cache instead
// of a direct client, so per-connection lifecycle comes from the framework.
type providerCache struct {
	consumer client.Client
	scheme   *runtime.Scheme

	mu      sync.Mutex
	clients map[string]client.Client
}

func newProviderCache(consumer client.Client, scheme *runtime.Scheme) *providerCache {
	return &providerCache{consumer: consumer, scheme: scheme, clients: map[string]client.Client{}}
}

func (p *providerCache) For(ctx context.Context, connName string) (client.Client, *corev1alpha1.Connection, error) {
	conn := &corev1alpha1.Connection{}
	if err := p.consumer.Get(ctx, client.ObjectKey{Name: connName}, conn); err != nil {
		return nil, nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if cl, ok := p.clients[connName]; ok {
		return cl, conn, nil
	}
	cl, err := remote.ProviderClient(ctx, p.consumer, conn, p.scheme)
	if err != nil {
		return nil, nil, err
	}
	p.clients[connName] = cl
	return cl, conn, nil
}

// specReconciler syncs instances of one GVR: spec consumer->provider (SSA),
// status provider->consumer, with a finalizer and ownership markers.
type specReconciler struct {
	gvr   schema.GroupVersionResource
	gvk   schema.GroupVersionKind
	scope apiextensionsv1.ResourceScope

	consumerLister dynamiclister.Lister
	consumerClient client.Client // direct, uncached
	providers      *providerCache
	recorder       record.EventRecorder
}

func (r *specReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("gvr", r.gvr.String(), "key", req.String())

	obj, err := r.getConsumerObject(req)
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	obj = obj.DeepCopy()

	crdName := CRDNameForGVR(r.gvr)
	res, err := ResolveConnection(ctx, r.consumerClient, crdName, req.Namespace)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("resolving connection: %w", err)
	}
	if !res.Found || !res.Ready {
		// Not bound (or binding not ready): nothing to do; binding changes will
		// re-trigger. If the object carries our finalizer with no binding, we
		// still try to release it on deletion below.
		if obj.GetDeletionTimestamp() == nil {
			return reconcile.Result{}, nil
		}
	}

	var (
		providerClient client.Client
		conn           *corev1alpha1.Connection
	)
	if res.Found && res.Ready {
		providerClient, conn, err = r.providers.For(ctx, res.ConnectionName)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("provider client: %w", err)
		}
	}

	localUID := ""
	if conn != nil {
		localUID = conn.Status.LocalClusterUID
	}

	// Deletion.
	if obj.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, obj, providerClient, localUID)
	}

	// Ensure finalizer before first write.
	if controllerutil.AddFinalizer(obj, corev1alpha1.FinalizerSyncer) {
		if err := r.consumerClient.Update(ctx, obj); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return reconcile.Result{Requeue: true}, nil
	}

	if providerClient == nil {
		return reconcile.Result{}, nil
	}

	// Conflict check before first write.
	existing := newUnstructured(r.gvk)
	getErr := providerClient.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	switch {
	case getErr == nil:
		if owner, ours := ownershipOf(existing, localUID, string(obj.GetUID())); !ours {
			reason := conflictReason(owner)
			msg := fmt.Sprintf("provider object %s already exists and is %s; not overwriting (conflictPolicy: Fail)", client.ObjectKeyFromObject(obj), owner)
			log.Info("conflict: refusing to overwrite provider object", "owner", owner)
			// Surface on the object's own condition (best-effort: pruned if the
			// CRD's status schema has no conditions) AND as an Event (always).
			setObjCondition(obj, metav1.ConditionFalse, reason, msg)
			_ = r.consumerClient.Status().Update(ctx, obj)
			if r.recorder != nil {
				r.recorder.Event(obj, corev1.EventTypeWarning, reason, msg)
			}
			return reconcile.Result{}, nil
		}
	case apierrors.IsNotFound(getErr):
		if r.scope == apiextensionsv1.NamespaceScoped {
			if err := ensureNamespace(ctx, providerClient, obj.GetNamespace(), localUID); err != nil {
				return reconcile.Result{}, err
			}
		}
	default:
		return reconcile.Result{}, fmt.Errorf("provider get: %w", getErr)
	}

	// Spec consumer -> provider (SSA).
	patch := r.providerPatch(obj, localUID)
	if err := providerClient.Patch(ctx, patch, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return reconcile.Result{}, fmt.Errorf("applying spec to provider: %w", err)
	}

	// Status provider -> consumer.
	got := newUnstructured(r.gvk)
	if err := providerClient.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
		return reconcile.Result{}, fmt.Errorf("reading provider status: %w", err)
	}
	if err := r.copyStatus(ctx, got, obj); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: statusResyncInterval}, nil
}

func (r *specReconciler) reconcileDelete(ctx context.Context, obj *unstructured.Unstructured, providerClient client.Client, localUID string) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(obj, corev1alpha1.FinalizerSyncer) {
		return reconcile.Result{}, nil
	}
	if providerClient != nil {
		existing := newUnstructured(r.gvk)
		err := providerClient.Get(ctx, client.ObjectKeyFromObject(obj), existing)
		switch {
		case err == nil:
			// Only delete the provider copy if it is ours. A foreign object that
			// merely shares the name (a conflict) must never be deleted by us.
			if _, ours := ownershipOf(existing, localUID, string(obj.GetUID())); ours {
				if err := providerClient.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
					return reconcile.Result{}, fmt.Errorf("deleting provider object: %w", err)
				}
				// Wait for the provider copy to be fully gone before releasing.
				return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
			}
		case !apierrors.IsNotFound(err):
			return reconcile.Result{}, fmt.Errorf("provider get during delete: %w", err)
		}
	}
	controllerutil.RemoveFinalizer(obj, corev1alpha1.FinalizerSyncer)
	if err := r.consumerClient.Update(ctx, obj); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return reconcile.Result{}, nil
}

func (r *specReconciler) getConsumerObject(req reconcile.Request) (*unstructured.Unstructured, error) {
	if r.scope == apiextensionsv1.NamespaceScoped {
		return r.consumerLister.Namespace(req.Namespace).Get(req.Name)
	}
	return r.consumerLister.Get(req.Name)
}

// providerPatch builds the SSA patch: spec + identity, ownership markers, no
// status, no consumer-only metadata.
func (r *specReconciler) providerPatch(obj *unstructured.Unstructured, localUID string) *unstructured.Unstructured {
	patch := newUnstructured(r.gvk)
	patch.SetName(obj.GetName())
	patch.SetNamespace(obj.GetNamespace())
	patch.SetLabels(map[string]string{corev1alpha1.LabelManaged: "true"})
	patch.SetAnnotations(map[string]string{
		corev1alpha1.AnnotationConsumerClusterUID: localUID,
		corev1alpha1.AnnotationConsumerObjectUID:  string(obj.GetUID()),
	})
	if spec, ok, _ := unstructured.NestedFieldCopy(obj.Object, "spec"); ok {
		_ = unstructured.SetNestedField(patch.Object, spec, "spec")
	}
	return patch
}

func (r *specReconciler) copyStatus(ctx context.Context, from, to *unstructured.Unstructured) error {
	status, ok, err := unstructured.NestedFieldCopy(from.Object, "status")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := unstructured.SetNestedField(to.Object, status, "status"); err != nil {
		return err
	}
	return r.consumerClient.Status().Update(ctx, to)
}

func newUnstructured(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

// ownershipOf reports the marker owner of a provider object and whether it is
// ours (our consumer cluster + our consumer object UID).
func ownershipOf(obj *unstructured.Unstructured, localUID, consumerObjUID string) (string, bool) {
	ann := obj.GetAnnotations()
	cluster := ann[corev1alpha1.AnnotationConsumerClusterUID]
	objUID := ann[corev1alpha1.AnnotationConsumerObjectUID]
	if cluster == "" && objUID == "" {
		return "foreign-unmanaged", false
	}
	if cluster == localUID && objUID == consumerObjUID {
		return "ours", true
	}
	return "owned-by-another", false
}

func conflictReason(owner string) string {
	if owner == "foreign-unmanaged" {
		return corev1alpha1.ReasonForeignObjectExists
	}
	return corev1alpha1.ReasonOwnedByAnother
}

func setObjCondition(obj *unstructured.Unstructured, status metav1.ConditionStatus, reason, msg string) {
	cond := map[string]any{
		"type":    corev1alpha1.ConditionSynced,
		"status":  string(status),
		"reason":  reason,
		"message": msg,
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	// Replace any existing Synced condition.
	out := make([]any, 0, len(conds)+1)
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && m["type"] == corev1alpha1.ConditionSynced {
			continue
		}
		out = append(out, c)
	}
	out = append(out, cond)
	_ = unstructured.SetNestedSlice(obj.Object, out, "status", "conditions")
}

func ensureNamespace(ctx context.Context, c client.Client, name, localUID string) error {
	if name == "" {
		return nil
	}
	ns := &unstructured.Unstructured{}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
	ns.SetName(name)
	ns.SetLabels(map[string]string{
		corev1alpha1.LabelManaged:                 "true",
		corev1alpha1.AnnotationConsumerClusterUID: localUID,
	})
	err := c.Create(ctx, ns)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
