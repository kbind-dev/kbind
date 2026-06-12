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

// Package connection reconciles Connection objects: it resolves the provider
// credentials, pins the provider/consumer cluster identity, and discovers the
// APIs the provider exports to those credentials.
package connection

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kube-bind/kube-bind/v2/konnector/engine/remote"
	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// Reconciler reconciles Connection objects on the consumer cluster.
type Reconciler struct {
	// Client is the consumer-cluster client.
	Client client.Client
	// Scheme is used to build the provider-cluster client.
	Scheme *runtime.Scheme
	// NewProviderClient builds a direct client for the provider cluster from a
	// Connection. Overridable in tests; defaults to a fresh client from the
	// resolved kubeconfig.
	NewProviderClient func(ctx context.Context, conn *corev1alpha1.Connection) (client.Client, error)
}

// SetupWithManager registers the reconciler with the consumer manager. It also
// watches Secrets so a Connection created before its kubeconfig Secret resolves
// as soon as the Secret arrives (order-independent "one apply").
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()
	r.Scheme = mgr.GetScheme()
	if r.NewProviderClient == nil {
		r.NewProviderClient = r.defaultProviderClient
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Connection{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.connectionsForSecret)).
		Named("connection").
		Complete(r)
}

// connectionsForSecret enqueues every Connection whose kubeconfigSecretRef
// points at the given Secret.
func (r *Reconciler) connectionsForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	var list corev1alpha1.ConnectionList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		ref := list.Items[i].Spec.KubeconfigSecretRef
		if ref.Namespace == secret.GetNamespace() && ref.Name == secret.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return reqs
}

func (r *Reconciler) defaultProviderClient(ctx context.Context, conn *corev1alpha1.Connection) (client.Client, error) {
	cfg, err := remote.RestConfigFromConnection(ctx, r.Client, conn)
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: r.Scheme})
}

// Reconcile drives a Connection toward Ready: SecretValid, Connected (identity
// pinned), and exportedAPIs discovered.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	conn := &corev1alpha1.Connection{}
	if err := r.Client.Get(ctx, req.NamespacedName, conn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if conn.DeletionTimestamp != nil {
		return r.reconcileDelete(ctx, conn)
	}

	// Ensure cleanup finalizers on the Connection and (best effort) its Secret
	// before doing any work, so teardown can always unwind.
	if added, err := r.ensureFinalizers(ctx, conn); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{}, nil
	}

	orig := conn.DeepCopy()
	reconcileErr := r.reconcile(ctx, conn)

	if !apiequalStatus(orig, conn) {
		if err := r.Client.Status().Update(ctx, conn); err != nil {
			log.Error(err, "updating Connection status")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, reconcileErr
}

func (r *Reconciler) reconcile(ctx context.Context, conn *corev1alpha1.Connection) error {
	// 1. Build the provider client (validates the secret + kubeconfig).
	providerClient, err := r.NewProviderClient(ctx, conn)
	if err != nil {
		setCondition(conn, corev1alpha1.ConditionSecretValid, metav1.ConditionFalse, corev1alpha1.ReasonSecretNotFound, err.Error())
		setNotReady(conn, corev1alpha1.ReasonSecretNotFound, "kubeconfig secret not usable yet")
		return nil // level-triggered: resolves when the secret arrives
	}
	setCondition(conn, corev1alpha1.ConditionSecretValid, metav1.ConditionTrue, corev1alpha1.ReasonAsExpected, "kubeconfig secret resolved")

	// 2. Pin and verify cluster identity.
	remoteUID, err := remote.ClusterUID(ctx, providerClient)
	if err != nil {
		setCondition(conn, corev1alpha1.ConditionConnected, metav1.ConditionFalse, corev1alpha1.ReasonPending, err.Error())
		setNotReady(conn, corev1alpha1.ReasonPending, "cannot reach provider cluster")
		return nil
	}
	localUID, err := remote.ClusterUID(ctx, r.Client)
	if err != nil {
		return fmt.Errorf("determining local cluster identity: %w", err)
	}
	if conn.Status.RemoteClusterUID != "" && conn.Status.RemoteClusterUID != remoteUID {
		setCondition(conn, corev1alpha1.ConditionConnected, metav1.ConditionFalse, corev1alpha1.ReasonClusterIdentityChanged,
			fmt.Sprintf("kubeconfig now points at cluster %s, but %s was pinned", remoteUID, conn.Status.RemoteClusterUID))
		setNotReady(conn, corev1alpha1.ReasonClusterIdentityChanged, "provider cluster identity changed")
		return nil
	}
	conn.Status.RemoteClusterUID = remoteUID
	if conn.Status.LocalClusterUID == "" {
		conn.Status.LocalClusterUID = localUID
	}
	setCondition(conn, corev1alpha1.ConditionConnected, metav1.ConditionTrue, corev1alpha1.ReasonAsExpected, "connected to provider")

	// 3. Discover exported APIs (schema.source: CRD — label-gated).
	exported, err := discoverExportedCRDs(ctx, providerClient)
	if err != nil {
		return fmt.Errorf("discovering exported APIs: %w", err)
	}
	conn.Status.ExportedAPIs = exported

	setCondition(conn, corev1alpha1.ConditionReady, metav1.ConditionTrue, corev1alpha1.ReasonAsExpected, "connection ready")
	return nil
}

// ensureFinalizers adds the cleanup finalizer to the Connection and to its
// referenced Secret (so the credential outlives the Connection during a
// `delete -f` teardown). Returns true if it changed the Connection.
func (r *Reconciler) ensureFinalizers(ctx context.Context, conn *corev1alpha1.Connection) (bool, error) {
	// Secret finalizer (best effort: the Secret may not exist yet).
	ref := conn.Spec.KubeconfigSecretRef
	var secret corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &secret); err == nil {
		if controllerutil.AddFinalizer(&secret, corev1alpha1.FinalizerCleanup) {
			if err := r.Client.Update(ctx, &secret); err != nil {
				return false, fmt.Errorf("adding finalizer to secret: %w", err)
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}

	if controllerutil.AddFinalizer(conn, corev1alpha1.FinalizerCleanup) {
		if err := r.Client.Update(ctx, conn); err != nil {
			return false, fmt.Errorf("adding finalizer to connection: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// reconcileDelete blocks the Connection from finalizing until no Binding
// references it (so binding cleanup can still reach the provider through it),
// then releases the Secret's finalizer and its own.
func (r *Reconciler) reconcileDelete(ctx context.Context, conn *corev1alpha1.Connection) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(conn, corev1alpha1.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	refs, err := r.bindingsReferencing(ctx, conn.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refs > 0 {
		setCondition(conn, corev1alpha1.ConditionReady, metav1.ConditionFalse, "DrainingBindings",
			fmt.Sprintf("waiting for %d binding(s) to finish unbinding", refs))
		if err := r.Client.Status().Update(ctx, conn); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Release the Secret finalizer — but only if no other Connection still
	// references the same Secret.
	ref := conn.Spec.KubeconfigSecretRef
	if others, err := r.otherConnectionsUsingSecret(ctx, ref, conn.Name); err != nil {
		return ctrl.Result{}, err
	} else if others == 0 {
		var secret corev1.Secret
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &secret); err == nil {
			if controllerutil.RemoveFinalizer(&secret, corev1alpha1.FinalizerCleanup) {
				if err := r.Client.Update(ctx, &secret); err != nil {
					return ctrl.Result{}, fmt.Errorf("releasing secret finalizer: %w", err)
				}
			}
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(conn, corev1alpha1.FinalizerCleanup)
	if err := r.Client.Update(ctx, conn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// otherConnectionsUsingSecret counts Connections other than self whose
// kubeconfigSecretRef points at the same Secret, so we don't release the
// Secret finalizer while another Connection still depends on it.
func (r *Reconciler) otherConnectionsUsingSecret(ctx context.Context, ref corev1alpha1.SecretKeyRef, selfName string) (int, error) {
	var list corev1alpha1.ConnectionList
	if err := r.Client.List(ctx, &list); err != nil {
		return 0, err
	}
	var n int
	for i := range list.Items {
		c := &list.Items[i]
		if c.Name == selfName || c.DeletionTimestamp != nil {
			continue
		}
		if c.Spec.KubeconfigSecretRef.Namespace == ref.Namespace && c.Spec.KubeconfigSecretRef.Name == ref.Name {
			n++
		}
	}
	return n, nil
}

// bindingsReferencing counts ClusterBindings and Bindings whose connectionRef
// names this Connection.
func (r *Reconciler) bindingsReferencing(ctx context.Context, name string) (int, error) {
	var n int
	var cbs corev1alpha1.ClusterBindingList
	if err := r.Client.List(ctx, &cbs); err != nil {
		return 0, err
	}
	for i := range cbs.Items {
		if cbs.Items[i].Spec.ConnectionRef.Name == name {
			n++
		}
	}
	var bs corev1alpha1.BindingList
	if err := r.Client.List(ctx, &bs); err != nil {
		return 0, err
	}
	for i := range bs.Items {
		if bs.Items[i].Spec.ConnectionRef.Name == name {
			n++
		}
	}
	return n, nil
}

// discoverExportedCRDs lists provider CRDs carrying the exported label and maps
// them to ExportedAPI entries.
func discoverExportedCRDs(ctx context.Context, providerClient client.Client) ([]corev1alpha1.ExportedAPI, error) {
	var crds apiextensionsv1.CustomResourceDefinitionList
	if err := providerClient.List(ctx, &crds, client.MatchingLabels{corev1alpha1.LabelExported: "true"}); err != nil {
		return nil, err
	}

	out := make([]corev1alpha1.ExportedAPI, 0, len(crds.Items))
	for i := range crds.Items {
		crd := &crds.Items[i]
		versions := make([]string, 0, len(crd.Spec.Versions))
		for _, v := range crd.Spec.Versions {
			if v.Served {
				versions = append(versions, v.Name)
			}
		}
		if len(versions) == 0 {
			continue
		}
		out = append(out, corev1alpha1.ExportedAPI{
			Name:     crd.Name,
			Group:    crd.Spec.Group,
			Resource: crd.Spec.Names.Plural,
			Scope:    crd.Spec.Scope,
			Versions: versions,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func setCondition(conn *corev1alpha1.Connection, condType string, status metav1.ConditionStatus, reason, msg string) {
	apimeta.SetStatusCondition(&conn.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: conn.Generation,
	})
}

func setNotReady(conn *corev1alpha1.Connection, reason, msg string) {
	setCondition(conn, corev1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg)
}

func apiequalStatus(a, b *corev1alpha1.Connection) bool {
	return apiequal(a.Status, b.Status)
}

// apiequal compares two ConnectionStatus values ignoring condition timestamps.
func apiequal(a, b corev1alpha1.ConnectionStatus) bool {
	if a.RemoteClusterUID != b.RemoteClusterUID || a.LocalClusterUID != b.LocalClusterUID {
		return false
	}
	if len(a.ExportedAPIs) != len(b.ExportedAPIs) {
		return false
	}
	for i := range a.ExportedAPIs {
		if a.ExportedAPIs[i].Name != b.ExportedAPIs[i].Name {
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
