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
)

// Binding activates instance sync for one or more exported APIs within its own
// namespace. Namespaced, following the Role convention. It is the v2 answer to
// v1's informerScope: Namespaced. Where a ClusterBinding and a Binding cover the
// same API on the same connection, the ClusterBinding wins.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories=kbind
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Connection",type=string,JSONPath=`.spec.connectionRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Binding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required
	Spec BindingSpec `json:"spec"`

	// +optional
	Status BindingStatus `json:"status,omitempty"`
}

// BindingList contains a list of Binding.
//
// +kubebuilder:object:root=true
type BindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Binding `json:"items"`
}
