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

// Connection is the link to one provider cluster. It owns the credentials and
// schema delivery, and surfaces what the provider exports. Cluster-scoped.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=kube-bind
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.spec.kubeconfigSecretRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required
	Spec ConnectionSpec `json:"spec"`

	// +optional
	Status ConnectionStatus `json:"status,omitempty"`
}

// ConnectionSpec defines the desired provider link.
type ConnectionSpec struct {
	// kubeconfigSecretRef points at the Secret holding the provider kubeconfig.
	// Immutable. The only credential reference in the core.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="kubeconfigSecretRef is immutable"
	KubeconfigSecretRef SecretKeyRef `json:"kubeconfigSecretRef"`

	// schema controls how exported APIs reach the consumer.
	//
	// +optional
	// +kubebuilder:default={}
	Schema SchemaPolicy `json:"schema,omitempty"`

	// autoBind, when true, makes the konnector maintain a managed ClusterBinding
	// (named after this Connection) covering all exported APIs.
	//
	// +kubebuilder:default=false
	AutoBind bool `json:"autoBind,omitempty"`
}

// SchemaPolicy controls schema source, pull and update behavior.
type SchemaPolicy struct {
	// source selects how schemas are obtained:
	//   Auto    - CRD if readable on the provider, else OpenAPI (default).
	//   CRD     - read apiextensions CRDs verbatim.
	//   OpenAPI - synthesize CRDs from discovery + /openapi/v3.
	//
	// +kubebuilder:validation:Enum=Auto;CRD;OpenAPI
	// +kubebuilder:default=Auto
	Source SchemaSource `json:"source,omitempty"`

	// pullPolicy selects which exported APIs are installed:
	//   Bound - only APIs referenced by a Binding/ClusterBinding (default).
	//   All   - every exported API readable by the credentials.
	//   None  - never install CRDs (user/extension manages them).
	//
	// +kubebuilder:validation:Enum=Bound;All;None
	// +kubebuilder:default=Bound
	PullPolicy PullPolicy `json:"pullPolicy,omitempty"`

	// updatePolicy selects whether installed schemas follow provider changes:
	//   Always - follow provider schema changes (default).
	//   Once   - pin at first pull.
	//
	// +kubebuilder:validation:Enum=Always;Once
	// +kubebuilder:default=Always
	UpdatePolicy UpdatePolicy `json:"updatePolicy,omitempty"`
}

// SchemaSource selects how schemas are obtained from the provider.
type SchemaSource string

const (
	// SchemaSourceAuto probes CRD first, falls back to OpenAPI.
	SchemaSourceAuto SchemaSource = "Auto"
	// SchemaSourceCRD reads apiextensions CRDs from the provider.
	SchemaSourceCRD SchemaSource = "CRD"
	// SchemaSourceOpenAPI synthesizes CRDs from discovery + /openapi/v3.
	SchemaSourceOpenAPI SchemaSource = "OpenAPI"
)

// PullPolicy selects which exported APIs are installed on the consumer.
type PullPolicy string

const (
	// PullPolicyBound installs only APIs referenced by a binding.
	PullPolicyBound PullPolicy = "Bound"
	// PullPolicyAll installs every exported API.
	PullPolicyAll PullPolicy = "All"
	// PullPolicyNone never installs CRDs.
	PullPolicyNone PullPolicy = "None"
)

// UpdatePolicy selects whether installed schemas track provider changes.
type UpdatePolicy string

const (
	// UpdatePolicyAlways follows provider schema changes.
	UpdatePolicyAlways UpdatePolicy = "Always"
	// UpdatePolicyOnce pins the schema at first pull.
	UpdatePolicyOnce UpdatePolicy = "Once"
)

// ConnectionStatus is the observed state of a Connection.
type ConnectionStatus struct {
	// remoteClusterUID is the identity of the provider cluster, pinned on first
	// connect and immutable thereafter. A Secret later pointing at a different
	// cluster is rejected rather than silently re-homing synced objects.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="remoteClusterUID is immutable once set"
	RemoteClusterUID string `json:"remoteClusterUID,omitempty"`

	// localClusterUID is the identity of the consumer cluster, pinned on first
	// connect and immutable thereafter.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="localClusterUID is immutable once set"
	LocalClusterUID string `json:"localClusterUID,omitempty"`

	// activeSchemaSource is the schema source actually in effect after resolving
	// "Auto" (CRD or OpenAPI). The binding uses it to decide whether the
	// Connection already installed the CRDs (OpenAPI) or it should pull them (CRD).
	//
	// +optional
	ActiveSchemaSource SchemaSource `json:"activeSchemaSource,omitempty"`

	// exportedAPIs is the discovery result: APIs exported to these credentials.
	//
	// +optional
	// +listType=atomic
	ExportedAPIs []ExportedAPI `json:"exportedAPIs,omitempty"`

	// conditions: SecretValid, Connected, SchemaInSync, Ready.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ConnectionList contains a list of Connection.
//
// +kubebuilder:object:root=true
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}
