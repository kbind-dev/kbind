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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// BindingAccessor is implemented by both ClusterBinding and Binding so a single
// reconciler can drive either kind. The only difference between them is scope.
// It is also a client.Object (metav1.Object + runtime.Object).
//
// +kubebuilder:object:generate=false
type BindingAccessor interface {
	metav1.Object
	runtime.Object
	// BindingSpecP returns a pointer to the shared spec.
	BindingSpecP() *BindingSpec
	// BindingStatusP returns a pointer to the shared status.
	BindingStatusP() *BindingStatus
}

// BindingSpecP returns a pointer to the ClusterBinding spec.
func (b *ClusterBinding) BindingSpecP() *BindingSpec { return &b.Spec }

// BindingStatusP returns a pointer to the ClusterBinding status.
func (b *ClusterBinding) BindingStatusP() *BindingStatus { return &b.Status }

// BindingSpecP returns a pointer to the Binding spec.
func (b *Binding) BindingSpecP() *BindingSpec { return &b.Spec }

// BindingStatusP returns a pointer to the Binding status.
func (b *Binding) BindingStatusP() *BindingStatus { return &b.Status }

var (
	_ BindingAccessor = &ClusterBinding{}
	_ BindingAccessor = &Binding{}
)

// ExportsAPI reports whether the Connection exports an API with the given CRD
// name ("<plural>.<group>").
func (s *ConnectionStatus) ExportsAPI(crdName string) (ExportedAPI, bool) {
	for i := range s.ExportedAPIs {
		if s.ExportedAPIs[i].Name == crdName {
			return s.ExportedAPIs[i], true
		}
	}
	return ExportedAPI{}, false
}
