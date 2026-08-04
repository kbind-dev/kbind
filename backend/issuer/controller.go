/*
Copyright 2026 The Kbind Authors.

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

package issuer

import (
	"context"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// GrantReconciler drives Grants through the Issuer: provision on existence,
// revoke on deletion (cleanup finalizer).
type GrantReconciler struct {
	Client client.Client
	Issuer Issuer
}

// SetupWithManager registers the reconciler with the provider-side manager.
func (r *GrantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&iamv1alpha1.Grant{}).
		Named("grant").
		Complete(r)
}

// Reconcile provisions the Grant's credentials and reports them in status.
func (r *GrantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	grant := &iamv1alpha1.Grant{}
	if err := r.Client.Get(ctx, req.NamespacedName, grant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if grant.DeletionTimestamp != nil {
		if !controllerutil.ContainsFinalizer(grant, iamv1alpha1.FinalizerCleanup) {
			return ctrl.Result{}, nil
		}
		if err := r.Issuer.Revoke(ctx, grant); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(grant, iamv1alpha1.FinalizerCleanup)
		return ctrl.Result{}, client.IgnoreNotFound(r.Client.Update(ctx, grant))
	}

	if controllerutil.AddFinalizer(grant, iamv1alpha1.FinalizerCleanup) {
		if err := r.Client.Update(ctx, grant); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	result, err := r.Issuer.Provision(ctx, grant)
	if err != nil {
		return ctrl.Result{}, err
	}

	orig := grant.DeepCopy()
	grant.Status.Namespace = result.Namespace
	grant.Status.ServiceAccount = result.ServiceAccount
	grant.Status.TokenSecret = result.TokenSecret
	if result.TokenReady {
		apimeta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
			Type: iamv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
			Reason: iamv1alpha1.ReasonAsExpected, Message: "credentials provisioned",
			ObservedGeneration: grant.Generation,
		})
	} else {
		apimeta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
			Type: iamv1alpha1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: iamv1alpha1.ReasonPending, Message: "waiting for the token controller to populate the token secret",
			ObservedGeneration: grant.Generation,
		})
	}
	if !apimeta.IsStatusConditionPresentAndEqual(orig.Status.Conditions, iamv1alpha1.ConditionReady, metav1.ConditionTrue) ||
		orig.Status.Namespace != grant.Status.Namespace || orig.Status.TokenSecret != grant.Status.TokenSecret {
		if err := r.Client.Status().Update(ctx, grant); err != nil {
			return ctrl.Result{}, err
		}
	}

	if !result.TokenReady {
		// The token controller populates the Secret out of band; poll briefly.
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}
