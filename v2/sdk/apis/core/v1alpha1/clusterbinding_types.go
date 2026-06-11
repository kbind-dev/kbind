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

// ClusterBinding activates instance sync for one or more exported APIs
// cluster-wide. Cluster-scoped, following the ClusterRole convention.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=kube-bind
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Connection",type=string,JSONPath=`.spec.connectionRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ClusterBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required
	Spec BindingSpec `json:"spec"`

	// +optional
	Status BindingStatus `json:"status,omitempty"`
}

// BindingStatus is the observed state shared by ClusterBinding and Binding.
type BindingStatus struct {
	// boundAPIs is per-API observed state.
	//
	// +optional
	// +listType=atomic
	BoundAPIs []BoundAPI `json:"boundAPIs,omitempty"`

	// conditions: Connected, Synced, Conflicts, PermissionDenied, Ready.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterBindingList contains a list of ClusterBinding.
//
// +kubebuilder:object:root=true
type ClusterBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterBinding `json:"items"`
}
