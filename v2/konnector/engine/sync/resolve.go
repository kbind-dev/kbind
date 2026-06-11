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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// CRDNameForGVR returns the "<plural>.<group>" CRD name for a GVR.
func CRDNameForGVR(gvr schema.GroupVersionResource) string {
	return gvr.Resource + "." + gvr.Group
}

// Resolution is the result of resolving which binding covers an API.
type Resolution struct {
	// Found is true if a binding lists the API.
	Found bool
	// Ready is true if that binding is Ready.
	Ready bool
	// ConnectionName is the Connection the binding references.
	ConnectionName string
}

// ResolveConnection finds the binding that covers crdName for the given
// namespace. A ClusterBinding wins over a namespaced Binding (proposal rule).
func ResolveConnection(ctx context.Context, c client.Client, crdName, namespace string) (Resolution, error) {
	var cbs corev1alpha1.ClusterBindingList
	if err := c.List(ctx, &cbs); err != nil {
		return Resolution{}, err
	}
	for i := range cbs.Items {
		cb := &cbs.Items[i]
		if listsAPI(cb.Spec.APIs, crdName) {
			return Resolution{
				Found:          true,
				Ready:          apimeta.IsStatusConditionTrue(cb.Status.Conditions, corev1alpha1.ConditionReady),
				ConnectionName: cb.Spec.ConnectionRef.Name,
			}, nil
		}
	}

	if namespace != "" {
		var bs corev1alpha1.BindingList
		if err := c.List(ctx, &bs, client.InNamespace(namespace)); err != nil {
			return Resolution{}, err
		}
		for i := range bs.Items {
			b := &bs.Items[i]
			if listsAPI(b.Spec.APIs, crdName) {
				return Resolution{
					Found:          true,
					Ready:          apimeta.IsStatusConditionTrue(b.Status.Conditions, corev1alpha1.ConditionReady),
					ConnectionName: b.Spec.ConnectionRef.Name,
				}, nil
			}
		}
	}

	return Resolution{Found: false}, nil
}

func listsAPI(apis []corev1alpha1.APIRef, crdName string) bool {
	for _, a := range apis {
		if a.Name == crdName {
			return true
		}
	}
	return false
}
