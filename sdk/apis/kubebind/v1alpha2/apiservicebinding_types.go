/*
Copyright 2025 The Kube Bind Authors.

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

package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsapi "github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

const (
	// APIServiceBindingConditionSecretValid is set when the secret is valid.
	APIServiceBindingConditionSecretValid conditionsapi.ConditionType = "SecretValid"

	// APIServiceBindingConditionInformersSynced is set when the informers can sync.
	APIServiceBindingConditionInformersSynced conditionsapi.ConditionType = "InformersSynced"

	// APIServiceBindingConditionHeartbeating is set when the ClusterBinding of the service provider
	// is successfully heartbeated.
	APIServiceBindingConditionHeartbeating conditionsapi.ConditionType = "Heartbeating"

	// APIServiceBindingConditionConnected means the APIServiceBinding has been connected to a APIServiceExport.
	APIServiceBindingConditionConnected conditionsapi.ConditionType = "Connected"

	// APIServiceBindingConditionSchemaInSync is set to true when the APIServiceExport's
	// schema is applied to the consumer cluster.
	APIServiceBindingConditionSchemaInSync conditionsapi.ConditionType = "SchemaInSync"

	// DownstreamFinalizer is put on downstream objects to block their deletion until
	// the upstream object has been deleted.
	DownstreamFinalizer = "kubebind.io/syncer"
)

// APIServiceBinding binds an API service represented by a APIServiceExport
// in a service provider cluster into a consumer cluster. This object lives in
// the consumer cluster.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kube-bindings,shortName=sb
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=`.status.providerPrettyName`,priority=0
// +kubebuilder:printcolumn:name="Resources",type="string",JSONPath=`.metadata.annotations.kube-bind\.io/resources`,priority=1
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`,priority=0
// +kubebuilder:printcolumn:name="Message",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].message`,priority=0
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=`.metadata.creationTimestamp`,priority=0
type APIServiceBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// spec specifies how an API service from a service provider should be bound in the
	// local consumer cluster.
	Spec APIServiceBindingSpec `json:"spec"`

	// status contains reconciliation information for a service binding.
	Status APIServiceBindingStatus `json:"status"`
}

func (in *APIServiceBinding) GetConditions() conditionsapi.Conditions {
	return in.Status.Conditions
}

func (in *APIServiceBinding) SetConditions(conditions conditionsapi.Conditions) {
	in.Status.Conditions = conditions
}

type APIServiceBindingSpec struct {
	// kubeconfigSecretName is the secret ref that contains the kubeconfig of the service cluster.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="kubeconfigSecretRef is immutable"
	KubeconfigSecretRef ClusterSecretKeyRef `json:"kubeconfigSecretRef"`

	// providerID is a stable and unique identifier for the provider cluster.
	//
	// +optional
	// +kubebuilder:validation:Optional
	ProviderID string `json:"providerID,omitempty"`

	// isDefault indicates whether this binding should be used as the default routing
	// destination for Custom Resources that do not specify a provider annotation.
	//
	// +optional
	// +kubebuilder:validation:Optional
	IsDefault bool `json:"isDefault,omitempty"`
}

type APIServiceBindingStatus struct {
	// providerPrettyName is the pretty name of the service provider cluster. This
	// can be shared among different APIServiceBindings.
	ProviderPrettyName string `json:"providerPrettyName,omitempty"`

	// conditions is a list of conditions that apply to the APIServiceBinding.
	Conditions conditionsapi.Conditions `json:"conditions,omitempty"`

	// BoundSchemas contains references to all BoundAPIResourceSchema objects
	// associated with this APIServiceBinding, tracking consumer usage status.
	// +optional
	// +kubebuilder:validation:MinItems=1
	BoundSchemas []BoundSchemaReference `json:"boundSchemas,omitempty"`

	// PermissionClaims records decisions about permission claims requested by the service provider.
	// Access is granted per GroupResource.
	PermissionClaims []PermissionClaim `json:"permissionClaims,omitempty"`
}

// BoundSchemaReference contains a reference to a BoundAPIResourceSchema with status information.
type BoundSchemaReference struct {
	GroupResource `json:",inline"`
}

// APIServiceBindingList is a list of APIServiceBindings.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIServiceBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIServiceBinding `json:"items"`
}

type LocalSecretKeyRef struct {
	// Name of the referent.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// The key of the secret to select from.
	//
	// +required
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

type ClusterSecretKeyRef struct {
	LocalSecretKeyRef `json:",inline"`

	// Namespace of the referent.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
}
