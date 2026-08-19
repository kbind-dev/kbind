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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Collection groups Exports for UI/CLI browsing. Pure presentation; nothing
// binds to a Collection.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=kbind
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Collection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required
	Spec CollectionSpec `json:"spec"`
}

// CollectionSpec is a titled grouping of Exports.
type CollectionSpec struct {
	// title is the human-facing name of the group.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Title string `json:"title"`

	// description explains the grouping.
	//
	// +optional
	Description string `json:"description,omitempty"`

	// exports lists the member Exports by name.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Exports []ExportRef `json:"exports"`
}

// ExportRef references an Export by name.
type ExportRef struct {
	// name of the Export.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// CollectionList contains a list of Collection.
//
// +kubebuilder:object:root=true
type CollectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Collection `json:"items"`
}
