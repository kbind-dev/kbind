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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

// Export is one curated offering in the provider's catalog: human-facing
// metadata plus defaults on top of one or more exported APIs. The catalog is
// derived-from-core-truth: an Export listing an API that is not actually
// exported (label/boundary) gets a condition and is hidden by the gateway —
// the export label remains the source of truth, the catalog is presentation
// and defaults. Lives on the provider; the konnector never sees it.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=kbind
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Export struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required
	Spec ExportSpec `json:"spec"`

	// +optional
	Status ExportStatus `json:"status,omitempty"`
}

// ExportSpec describes one offering.
type ExportSpec struct {
	// title is the human-facing name of the offering.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Title string `json:"title"`

	// description explains the offering to a human choosing it.
	//
	// +optional
	Description string `json:"description,omitempty"`

	// icon optionally points at an icon for UIs.
	//
	// +optional
	Icon *Icon `json:"icon,omitempty"`

	// docs optionally links to the offering's documentation.
	//
	// +optional
	Docs string `json:"docs,omitempty"`

	// apis lists the exported APIs a binding to this offering syncs, by CRD
	// name on the provider ("<plural>.<group>").
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	APIs []corev1alpha1.APIRef `json:"apis"`

	// defaults are copied into the generated ClusterBinding of the one-apply
	// bundle.
	//
	// +optional
	Defaults BindingDefaults `json:"defaults,omitempty"`
}

// Icon points at an icon image for UIs.
type Icon struct {
	// url of the icon image.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
}

// BindingDefaults are the binding fields an Export pre-fills in the generated
// bundle.
type BindingDefaults struct {
	// conflictPolicy is the default conflict behavior for the generated
	// ClusterBinding.
	//
	// +optional
	// +kubebuilder:validation:Enum=Fail;Adopt
	ConflictPolicy corev1alpha1.ConflictPolicy `json:"conflictPolicy,omitempty"`

	// relatedResources are the related-resource selectors for the generated
	// ClusterBinding.
	//
	// +optional
	// +listType=atomic
	RelatedResources []corev1alpha1.RelatedResource `json:"relatedResources,omitempty"`
}

// ExportStatus is the observed state of an Export.
type ExportStatus struct {
	// conditions: Ready (all listed APIs are actually exported).
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ExportList contains a list of Export.
//
// +kubebuilder:object:root=true
type ExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Export `json:"items"`
}
