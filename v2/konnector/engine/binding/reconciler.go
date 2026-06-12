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

// Package binding reconciles ClusterBinding and Binding objects: it validates
// the referenced Connection, pulls the CRDs for the listed APIs onto the
// consumer (schema.source: CRD), and reports per-API state.
package binding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kube-bind/kube-bind/v2/konnector/engine/remote"
	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// base holds the shared dependencies and logic for both binding kinds.
type base struct {
	client    client.Client
	dyn       dynamic.Interface
	scheme    *runtime.Scheme
	resync    time.Duration
	newClient func(ctx context.Context, conn *corev1alpha1.Connection) (client.Client, error)
}

// resyncInterval is how often a Ready binding re-reconciles to refresh
// conflictCount (conflicts are detected by the syncer, not via binding events).
func (b *base) resyncInterval() time.Duration {
	if b.resync > 0 {
		return b.resync
	}
	return 30 * time.Second
}

func (b *base) providerClient(ctx context.Context, conn *corev1alpha1.Connection) (client.Client, error) {
	if b.newClient != nil {
		return b.newClient(ctx, conn)
	}
	return remote.ProviderClient(ctx, b.client, conn, b.scheme)
}

// reconcileAccessor drives a binding (cluster or namespaced) toward Ready.
// namespace scopes conflict counting: "" for a ClusterBinding (cluster-wide),
// the Binding's namespace otherwise.
func (b *base) reconcileAccessor(ctx context.Context, obj corev1alpha1.BindingAccessor, namespace string) error {
	spec := obj.BindingSpecP()
	status := obj.BindingStatusP()

	// Resolve the Connection.
	conn := &corev1alpha1.Connection{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: spec.ConnectionRef.Name}, conn); err != nil {
		if apierrors.IsNotFound(err) {
			setReady(status, obj, metav1.ConditionFalse, corev1alpha1.ReasonPending,
				fmt.Sprintf("Connection %q not found yet", spec.ConnectionRef.Name))
			return nil
		}
		return fmt.Errorf("getting Connection %q: %w", spec.ConnectionRef.Name, err)
	}
	if !apimeta.IsStatusConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionReady) {
		setReady(status, obj, metav1.ConditionFalse, corev1alpha1.ReasonPending,
			fmt.Sprintf("Connection %q not ready yet", spec.ConnectionRef.Name))
		return nil
	}

	providerClient, err := b.providerClient(ctx, conn)
	if err != nil {
		return fmt.Errorf("building provider client: %w", err)
	}

	var (
		boundAPIs   []corev1alpha1.BoundAPI
		notExported []string
	)
	for _, api := range spec.APIs {
		exported, ok := conn.Status.ExportsAPI(api.Name)
		if !ok {
			notExported = append(notExported, api.Name)
			continue
		}
		hash, err := b.pullCRD(ctx, providerClient, api.Name, conn.Name)
		if err != nil {
			return fmt.Errorf("pulling CRD %q: %w", api.Name, err)
		}
		// Count instances we refused to sync due to a foreign provider target.
		var conflicts int32
		if gvr, ok, gerr := b.gvrFor(ctx, api.Name); gerr == nil && ok {
			conflicts = b.countConflicts(ctx, gvr, namespace)
		}
		boundAPIs = append(boundAPIs, corev1alpha1.BoundAPI{Name: exported.Name, CRDHash: hash, ConflictCount: conflicts})
	}
	sort.Slice(boundAPIs, func(i, j int) bool { return boundAPIs[i].Name < boundAPIs[j].Name })
	status.BoundAPIs = boundAPIs

	if len(notExported) > 0 {
		setReady(status, obj, metav1.ConditionFalse, corev1alpha1.ReasonAPINotExported,
			fmt.Sprintf("APIs not exported by the provider yet: %s", strings.Join(notExported, ", ")))
		return nil
	}

	var totalConflicts int32
	for _, ba := range boundAPIs {
		totalConflicts += ba.ConflictCount
	}
	if totalConflicts > 0 {
		apimeta.SetStatusCondition(&status.Conditions, condition(obj, corev1alpha1.ConditionConflicts, metav1.ConditionTrue, corev1alpha1.ReasonForeignObjectExists,
			fmt.Sprintf("%d object(s) skipped due to foreign ownership; see object Events", totalConflicts)))
	} else {
		apimeta.SetStatusCondition(&status.Conditions, condition(obj, corev1alpha1.ConditionConflicts, metav1.ConditionFalse, corev1alpha1.ReasonAsExpected, "no conflicts"))
	}

	apimeta.SetStatusCondition(&status.Conditions, condition(obj, corev1alpha1.ConditionSynced, metav1.ConditionTrue, corev1alpha1.ReasonAsExpected, "all APIs installed"))
	setReady(status, obj, metav1.ConditionTrue, corev1alpha1.ReasonAsExpected, "binding ready")
	return nil
}

// pullCRD reads the provider CRD by name and installs it on the consumer
// (create-if-absent; POC pulls once). It returns a content hash for status.
func (b *base) pullCRD(ctx context.Context, providerClient client.Client, crdName, connName string) (string, error) {
	var remoteCRD apiextensionsv1.CustomResourceDefinition
	if err := providerClient.Get(ctx, client.ObjectKey{Name: crdName}, &remoteCRD); err != nil {
		return "", fmt.Errorf("reading provider CRD: %w", err)
	}

	consumerCRD := crdForConsumer(&remoteCRD, connName)
	hash := crdHash(consumerCRD)

	var existing apiextensionsv1.CustomResourceDefinition
	err := b.client.Get(ctx, client.ObjectKey{Name: crdName}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := b.client.Create(ctx, consumerCRD); err != nil {
			return "", fmt.Errorf("creating consumer CRD: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("getting consumer CRD: %w", err)
	default:
		// Already present. updatePolicy: Once for the POC — do not update.
	}
	return hash, nil
}

// crdForConsumer strips the provider CRD down to something installable on the
// consumer without a conversion webhook: single served/storage version,
// conversion forced to None, no webhook/caBundle, no ownerRefs/status.
func crdForConsumer(in *apiextensionsv1.CustomResourceDefinition, connName string) *apiextensionsv1.CustomResourceDefinition {
	out := in.DeepCopy()
	out.ResourceVersion = ""
	out.UID = ""
	out.ManagedFields = nil
	out.OwnerReferences = nil
	out.Status = apiextensionsv1.CustomResourceDefinitionStatus{}

	// Keep only the storage version (fallback: first served), drop the rest, so
	// no conversion webhook is needed on the consumer.
	var keep *apiextensionsv1.CustomResourceDefinitionVersion
	for i := range out.Spec.Versions {
		v := &out.Spec.Versions[i]
		if v.Storage {
			keep = v
			break
		}
		if keep == nil && v.Served {
			keep = v
		}
	}
	if keep != nil {
		k := *keep
		k.Storage = true
		k.Served = true
		out.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{k}
	}
	out.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{Strategy: apiextensionsv1.NoneConverter}

	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	out.Labels[corev1alpha1.LabelManaged] = "true"
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	out.Annotations[corev1alpha1.AnnotationConnection] = connName
	return out
}

func crdHash(crd *apiextensionsv1.CustomResourceDefinition) string {
	h := sha256.New()
	for _, v := range crd.Spec.Versions {
		_, _ = h.Write([]byte(v.Name))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

func condition(obj client.Object, t string, s metav1.ConditionStatus, reason, msg string) metav1.Condition {
	return metav1.Condition{
		Type:               t,
		Status:             s,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: obj.GetGeneration(),
	}
}

func setReady(status *corev1alpha1.BindingStatus, obj client.Object, s metav1.ConditionStatus, reason, msg string) {
	apimeta.SetStatusCondition(&status.Conditions, condition(obj, corev1alpha1.ConditionReady, s, reason, msg))
}

// ----- ClusterBinding -----

// ClusterReconciler reconciles ClusterBinding objects.
type ClusterReconciler struct {
	base
	// Resync is how often a Ready binding re-reconciles to refresh conflictCount
	// (0 = default 30s).
	Resync time.Duration
}

// SetupWithManager registers the ClusterBinding reconciler. It also watches
// Connection so bindings re-reconcile when their connection becomes Ready or
// its exported APIs change (level-triggered).
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.base.client = mgr.GetClient()
	r.base.scheme = mgr.GetScheme()
	r.base.resync = r.Resync
	dyn, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return err
	}
	r.base.dyn = dyn
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.ClusterBinding{}).
		Watches(&corev1alpha1.Connection{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForConnection)).
		Named("clusterbinding").
		Complete(r)
}

func (r *ClusterReconciler) bindingsForConnection(ctx context.Context, conn client.Object) []reconcile.Request {
	var list corev1alpha1.ClusterBindingList
	if err := r.base.client.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.ConnectionRef.Name == conn.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return reqs
}

// Reconcile reconciles a ClusterBinding.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cb := &corev1alpha1.ClusterBinding{}
	if err := r.base.client.Get(ctx, req.NamespacedName, cb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cb.DeletionTimestamp != nil {
		if !controllerutil.ContainsFinalizer(cb, corev1alpha1.FinalizerCleanup) {
			return ctrl.Result{}, nil
		}
		// Cluster-wide unbind: clean up cluster-wide and remove the pulled CRDs.
		if err := r.base.cleanup(ctx, cb, "", true); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(cb, corev1alpha1.FinalizerCleanup)
		return ctrl.Result{}, client.IgnoreNotFound(r.base.client.Update(ctx, cb))
	}
	if controllerutil.AddFinalizer(cb, corev1alpha1.FinalizerCleanup) {
		return ctrl.Result{}, r.base.client.Update(ctx, cb)
	}
	orig := cb.DeepCopy()
	rerr := r.base.reconcileAccessor(ctx, cb, "")
	if !equalBindingStatus(&orig.Status, &cb.Status) {
		if err := r.base.client.Status().Update(ctx, cb); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: r.base.resyncInterval()}, rerr
}

// ----- Binding (namespaced) -----

// NamespacedReconciler reconciles Binding objects.
type NamespacedReconciler struct {
	base
	// Resync is how often a Ready binding re-reconciles to refresh conflictCount
	// (0 = default 30s).
	Resync time.Duration
}

// SetupWithManager registers the Binding reconciler. It also watches Connection
// so bindings re-reconcile when their connection becomes Ready (level-triggered).
func (r *NamespacedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.base.client = mgr.GetClient()
	r.base.scheme = mgr.GetScheme()
	r.base.resync = r.Resync
	dyn, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return err
	}
	r.base.dyn = dyn
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Binding{}).
		Watches(&corev1alpha1.Connection{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForConnection)).
		Named("binding").
		Complete(r)
}

func (r *NamespacedReconciler) bindingsForConnection(ctx context.Context, conn client.Object) []reconcile.Request {
	var list corev1alpha1.BindingList
	if err := r.base.client.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.ConnectionRef.Name == conn.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: list.Items[i].Namespace, Name: list.Items[i].Name}})
		}
	}
	return reqs
}

// Reconcile reconciles a Binding.
func (r *NamespacedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	b := &corev1alpha1.Binding{}
	if err := r.base.client.Get(ctx, req.NamespacedName, b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if b.DeletionTimestamp != nil {
		if !controllerutil.ContainsFinalizer(b, corev1alpha1.FinalizerCleanup) {
			return ctrl.Result{}, nil
		}
		// Namespaced unbind: clean up only instances in this namespace; the CRD
		// is shared and stays.
		if err := r.base.cleanup(ctx, b, b.Namespace, false); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(b, corev1alpha1.FinalizerCleanup)
		return ctrl.Result{}, client.IgnoreNotFound(r.base.client.Update(ctx, b))
	}
	if controllerutil.AddFinalizer(b, corev1alpha1.FinalizerCleanup) {
		return ctrl.Result{}, r.base.client.Update(ctx, b)
	}
	orig := b.DeepCopy()
	rerr := r.base.reconcileAccessor(ctx, b, b.Namespace)
	if !equalBindingStatus(&orig.Status, &b.Status) {
		if err := r.base.client.Status().Update(ctx, b); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: r.base.resyncInterval()}, rerr
}

func equalBindingStatus(a, b *corev1alpha1.BindingStatus) bool {
	if len(a.BoundAPIs) != len(b.BoundAPIs) {
		return false
	}
	for i := range a.BoundAPIs {
		if a.BoundAPIs[i] != b.BoundAPIs[i] {
			return false
		}
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		ac, bc := a.Conditions[i], b.Conditions[i]
		if ac.Type != bc.Type || ac.Status != bc.Status || ac.Reason != bc.Reason || ac.Message != bc.Message {
			return false
		}
	}
	return true
}
