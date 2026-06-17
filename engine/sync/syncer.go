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
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kbind/kbind/engine/mapper"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

const (
	// fieldOwner is the server-side-apply field manager for spec writes.
	fieldOwner = "kbind-konnector"
	// statusResyncBackstop is a low-frequency safety net. Provider-originated
	// changes (status, drift) are primarily delivered by the engaged-cluster
	// watch; this just heals any missed watch event.
	statusResyncBackstop = 10 * time.Minute
)

// specReconciler syncs instances of one GVR: spec consumer->provider (SSA),
// status provider->consumer, with a finalizer and ownership markers. The
// provider side is the multicluster-runtime engaged cluster for one Connection:
// writes go through its client, reads through its API reader (no cache
// staleness), and provider events arrive via a watch on its cache.
type specReconciler struct {
	gvr   schema.GroupVersionResource
	gvk   schema.GroupVersionKind
	scope apiextensionsv1.ResourceScope

	consumerLister dynamiclister.Lister
	consumerClient client.Client // direct, uncached

	providerClient client.Client // engaged-cluster client: SSA + delete
	providerReader client.Reader // engaged-cluster API reader: fresh reads
	localUID       string        // consumer cluster identity for ownership markers

	// mapper translates the consumer object key to its provider key. Core uses
	// mapper.Identity (ns/name unchanged); an out-of-tree build can swap it.
	mapper mapper.Mapper

	recorder record.EventRecorder
}

// providerSource turns the engaged provider cluster's cache into an event
// source: a provider object owned by us (matching localUID) enqueues its mapped
// consumer object. This is what makes status/drift sync event-driven instead of
// polled. The provider key is mapped back to the consumer key via the Mapper so
// the request targets the right consumer object under non-identity mappings.
func providerSource(c cache.Cache, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, localUID string, m mapper.Mapper) source.TypedSource[reconcile.Request] {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return source.Kind(c, client.Object(obj), handler.TypedEnqueueRequestsFromMapFunc(
		func(_ context.Context, o client.Object) []reconcile.Request {
			if o.GetAnnotations()[corev1alpha1.AnnotationConsumerClusterUID] != localUID {
				return nil // not ours (another consumer on a shared provider)
			}
			consumerKey, err := m.ToConsumer(gvr, client.ObjectKey{Namespace: o.GetNamespace(), Name: o.GetName()})
			if err != nil {
				return nil
			}
			return []reconcile.Request{{NamespacedName: consumerKey}}
		}))
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

	// Gate: is there a ready binding covering this object? (Namespaced Bindings
	// scope which namespaces sync; ClusterBindings cover everything.)
	res, err := ResolveConnection(ctx, r.consumerClient, CRDNameForGVR(r.gvr), req.Namespace)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("resolving connection: %w", err)
	}
	bound := res.Found && res.Ready

	// Deletion: always release our copy, even if the binding is already gone.
	if obj.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, obj)
	}
	if !bound {
		// Not bound yet; binding changes re-trigger this reconcile.
		return reconcile.Result{}, nil
	}

	// Map the consumer key to the provider key. Core maps identity; an out-of-tree
	// Mapper can rename/re-namespace. Every provider-side operation below uses
	// providerKey, never the consumer object's own key.
	providerKey, err := r.mapper.ToProvider(r.gvr, client.ObjectKeyFromObject(obj))
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("mapping %s to provider: %w", client.ObjectKeyFromObject(obj), err)
	}

	// Ensure finalizer before first write.
	if controllerutil.AddFinalizer(obj, corev1alpha1.FinalizerSyncer) {
		if err := r.consumerClient.Update(ctx, obj); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return reconcile.Result{Requeue: true}, nil
	}

	// Conflict check before first write (fresh read via the API reader).
	existing := newUnstructured(r.gvk)
	getErr := r.providerReader.Get(ctx, providerKey, existing)
	switch {
	case getErr == nil:
		if owner, ours := ownershipOf(existing, r.localUID, string(obj.GetUID())); !ours {
			// Adopt only takes an un-owned (markerless) object; it never steals
			// one already owned by another binding/consumer.
			if owner != ownerForeignUnmanaged || res.ConflictPolicy != corev1alpha1.ConflictPolicyAdopt {
				reason := conflictReason(owner)
				msg := fmt.Sprintf("provider object %s already exists and is %s; not overwriting (conflictPolicy: %s)", providerKey, owner, policyOrFail(res.ConflictPolicy))
				log.Info("conflict: refusing to overwrite provider object", "owner", owner)
				if err := r.markConflict(ctx, obj, reason, msg); err != nil {
					return reconcile.Result{}, err
				}
				return reconcile.Result{}, nil
			}
			log.Info("adopting un-owned provider object", "key", providerKey)
		}
	case apierrors.IsNotFound(getErr):
		if r.scope == apiextensionsv1.NamespaceScoped {
			if err := ensureNamespace(ctx, r.providerClient, providerKey.Namespace, r.localUID); err != nil {
				if apierrors.IsForbidden(err) {
					return r.permissionDenied(obj, "creating provider namespace", err), nil
				}
				return reconcile.Result{}, err
			}
		}
	default:
		return reconcile.Result{}, fmt.Errorf("provider get: %w", getErr)
	}

	// We are syncing — clear any stale conflict marker from a previous round.
	if err := r.clearConflict(ctx, obj); err != nil {
		return reconcile.Result{}, err
	}

	// Spec consumer -> provider (SSA).
	patch := r.providerPatch(obj, providerKey, r.localUID)
	if err := r.providerClient.Patch(ctx, patch, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		if apierrors.IsForbidden(err) {
			return r.permissionDenied(obj, "applying spec to provider", err), nil
		}
		return reconcile.Result{}, fmt.Errorf("applying spec to provider: %w", err)
	}

	// Status provider -> consumer.
	got := newUnstructured(r.gvk)
	if err := r.providerReader.Get(ctx, providerKey, got); err != nil {
		return reconcile.Result{}, fmt.Errorf("reading provider status: %w", err)
	}
	if err := r.copyStatus(ctx, got, obj); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: statusResyncBackstop}, nil
}

// markConflict records a conflict durably (an annotation that survives status
// pruning, used by bindings to count conflicts), best-effort on the object's
// own condition, and as an Event.
func (r *specReconciler) markConflict(ctx context.Context, obj *unstructured.Unstructured, reason, msg string) error {
	if obj.GetAnnotations()[corev1alpha1.AnnotationConflict] != reason {
		p := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`, corev1alpha1.AnnotationConflict, reason)
		if err := r.consumerClient.Patch(ctx, obj, client.RawPatch(types.MergePatchType, p)); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("marking conflict: %w", err)
		}
	}
	setObjCondition(obj, metav1.ConditionFalse, reason, msg)
	_ = r.consumerClient.Status().Update(ctx, obj)
	if r.recorder != nil {
		r.recorder.Event(obj, corev1.EventTypeWarning, reason, msg)
	}
	return nil
}

// permissionDenied surfaces a provider RBAC denial as an Event and requeues —
// it never fails the whole controller, so other objects keep syncing. The denial
// resolves on its own once RBAC is granted.
func (r *specReconciler) permissionDenied(obj *unstructured.Unstructured, op string, err error) reconcile.Result {
	if r.recorder != nil {
		r.recorder.Event(obj, corev1.EventTypeWarning, corev1alpha1.ReasonForbidden,
			fmt.Sprintf("%s is forbidden by provider RBAC: %v", op, err))
	}
	return reconcile.Result{RequeueAfter: 30 * time.Second}
}

// clearConflict removes the conflict marker once the object syncs cleanly.
func (r *specReconciler) clearConflict(ctx context.Context, obj *unstructured.Unstructured) error {
	if obj.GetAnnotations()[corev1alpha1.AnnotationConflict] == "" {
		return nil
	}
	p := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:null}}}`, corev1alpha1.AnnotationConflict)
	if err := r.consumerClient.Patch(ctx, obj, client.RawPatch(types.MergePatchType, p)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("clearing conflict: %w", err)
	}
	return nil
}

func (r *specReconciler) reconcileDelete(ctx context.Context, obj *unstructured.Unstructured) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(obj, corev1alpha1.FinalizerSyncer) {
		return reconcile.Result{}, nil
	}
	// deletion-policy: Orphan keeps the provider copy — just release the finalizer.
	if obj.GetAnnotations()[corev1alpha1.AnnotationDeletionPolicy] != corev1alpha1.DeletionPolicyOrphan {
		providerKey, err := r.mapper.ToProvider(r.gvr, client.ObjectKeyFromObject(obj))
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("mapping %s to provider: %w", client.ObjectKeyFromObject(obj), err)
		}
		existing := newUnstructured(r.gvk)
		err = r.providerReader.Get(ctx, providerKey, existing)
		switch {
		case err == nil:
			// Only delete the provider copy if it is ours. A foreign object that
			// merely shares the name (a conflict) must never be deleted by us.
			if _, ours := ownershipOf(existing, r.localUID, string(obj.GetUID())); ours {
				if err := r.providerClient.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
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
// status, no consumer-only metadata. The provider key (from the Mapper) sets the
// name/namespace, so a non-identity mapping writes to the mapped location while
// the ownership markers still record the consumer object's identity.
func (r *specReconciler) providerPatch(obj *unstructured.Unstructured, providerKey mapper.ObjectKey, localUID string) *unstructured.Unstructured {
	patch := newUnstructured(r.gvk)
	patch.SetName(providerKey.Name)
	patch.SetNamespace(providerKey.Namespace)
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

// Ownership classifications returned by ownershipOf.
const (
	ownerForeignUnmanaged = "foreign-unmanaged"
	ownerOurs             = "ours"
	ownerByAnother        = "owned-by-another"
)

// ownershipOf reports the marker owner of a provider object and whether it is
// ours (our consumer cluster + our consumer object UID).
func ownershipOf(obj *unstructured.Unstructured, localUID, consumerObjUID string) (string, bool) {
	ann := obj.GetAnnotations()
	cluster := ann[corev1alpha1.AnnotationConsumerClusterUID]
	objUID := ann[corev1alpha1.AnnotationConsumerObjectUID]
	if cluster == "" && objUID == "" {
		return ownerForeignUnmanaged, false
	}
	if cluster == localUID && objUID == consumerObjUID {
		return ownerOurs, true
	}
	return ownerByAnother, false
}

func conflictReason(owner string) string {
	if owner == ownerForeignUnmanaged {
		return corev1alpha1.ReasonForeignObjectExists
	}
	return corev1alpha1.ReasonOwnedByAnother
}

func policyOrFail(p corev1alpha1.ConflictPolicy) corev1alpha1.ConflictPolicy {
	if p == "" {
		return corev1alpha1.ConflictPolicyFail
	}
	return p
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
