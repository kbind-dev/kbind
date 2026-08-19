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

package issuer

import (
	"context"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/kbind/kbind/sdk/apis/catalog/v1alpha1"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

// ExportReconciler keeps the catalog derived-from-core-truth: an Export
// listing an API that is not actually exported (missing the export label)
// gets Ready=False, and the gateway hides it.
type ExportReconciler struct {
	Client client.Client
}

// SetupWithManager registers the reconciler; CRD changes re-validate every
// Export (labels flipping is exactly the event that matters).
func (r *ExportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&catalogv1alpha1.Export{}).
		Watches(&apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(r.allExports)).
		Named("catalog-export").
		Complete(r)
}

func (r *ExportReconciler) allExports(ctx context.Context, _ client.Object) []reconcile.Request {
	var list catalogv1alpha1.ExportList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
	}
	return reqs
}

// Reconcile validates the Export's API list against the actually exported CRDs.
func (r *ExportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	export := &catalogv1alpha1.Export{}
	if err := r.Client.Get(ctx, req.NamespacedName, export); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var crds apiextensionsv1.CustomResourceDefinitionList
	if err := r.Client.List(ctx, &crds, client.MatchingLabels{corev1alpha1.LabelExported: "true"}); err != nil {
		return ctrl.Result{}, err
	}
	exported := make(map[string]bool, len(crds.Items))
	for i := range crds.Items {
		exported[crds.Items[i].Name] = true
	}

	var missing []string
	for _, api := range export.Spec.APIs {
		if !exported[api.Name] {
			missing = append(missing, api.Name)
		}
	}

	cond := metav1.Condition{
		Type: catalogv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
		Reason: catalogv1alpha1.ReasonAsExpected, Message: "all listed APIs are exported",
		ObservedGeneration: export.Generation,
	}
	if len(missing) > 0 {
		cond.Status = metav1.ConditionFalse
		cond.Reason = catalogv1alpha1.ReasonAPINotExported
		cond.Message = fmt.Sprintf("not exported (missing the %s label): %s", corev1alpha1.LabelExported, strings.Join(missing, ", "))
	}
	if apimeta.SetStatusCondition(&export.Status.Conditions, cond) {
		if err := r.Client.Status().Update(ctx, export); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}
