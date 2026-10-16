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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	conditionsapi "github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

const (
	// APIServiceExportRequestConditionExportsReady is set to true when the
	// corresponding APIServiceExport is ready.
	APIServiceExportRequestConditionExportsReady conditionsapi.ConditionType = "ExportsReady"
)

// APIServiceExportRequest is represents a request session of kubectl-bind-apiservice.
//
// The service provider can prune these objects after some time.
//
// +crd
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Namespaced,categories=kube-bindings
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`,priority=0
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=`.metadata.creationTimestamp`,priority=0
type APIServiceExportRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// spec specifies how an API service from a service provider should be bound in the
	// local consumer cluster.
	//
	// +required
	// +kubebuilder:validation:Required
	Spec APIServiceExportRequestSpec `json:"spec"`

	// status contains reconciliation information for a service binding.
	// +kubebuilder:default={}
	Status APIServiceExportRequestStatus `json:"status"`
}

// APIServiceExportRequestResponse is like APIServiceExportRequest but without
// ObjectMeta, to avoid unwanted metadata fields being sent in the response.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIServiceExportRequestResponse struct {
	metav1.TypeMeta `json:",inline"`
	ObjectMeta      NameObjectMeta `json:"metadata"`

	// spec specifies how an API service from a service provider should be bound in the
	// local consumer cluster.
	//
	// +required
	// +kubebuilder:validation:Required
	Spec APIServiceExportRequestSpec `json:"spec"`

	// status contains reconciliation information for a service binding.
	Status APIServiceExportRequestStatus `json:"status,omitempty"`
}

type NameObjectMeta struct {
	// Name is the name of the object.
	Name string `json:"name,omitempty"`
}

func (in *APIServiceExportRequest) GetConditions() conditionsapi.Conditions {
	return in.Status.Conditions
}

func (in *APIServiceExportRequest) SetConditions(conditions conditionsapi.Conditions) {
	in.Status.Conditions = conditions
}

// APIServiceExportRequestSpec is the spec of a APIServiceExportRequest.
type APIServiceExportRequestSpec struct {
	// parameters holds service provider specific parameters for this binding
	// request.
	//
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="parameters are immutable"
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`

	// resources is a list of resources that should be exported.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:XListType=map
	// +kubebuilder:validation:XListMapKey=resource
	// +kubebuilder:validation:XListMapKey=group
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="resources are immutable"
	Resources []APIServiceExportRequestResource `json:"resources"`

	// PermissionClaims records decisions about permission claims requested by the service provider.
	// Access is granted per GroupResource.
	//
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="permissionClaims are immutable"
	// +kubebuilder:validation:XListType=map
	// +kubebuilder:validation:XListMapKey=resource
	// +kubebuilder:validation:XListMapKey=group
	// +kubebuilder:validation:XValidation:rule="self.all(c, (c.group == '' && c.resource == 'configmaps') || (c.group == '' && c.resource == 'secrets') || (c.group == '' && c.resource == 'serviceaccounts'))",message="Resource is not a valid claimable API"
	PermissionClaims []PermissionClaim `json:"permissionClaims,omitempty"`
	// namespaces specifies the namespaces to bootstrap as part of this request.
	// When objects originate from provider side, the consumer does not always know the necessary details.
	// This field allows provider to pre-heat the necessary namespaces on provider side by creating
	// APIServiceNamespace objects attached to the APIServiceExport. More namespaces can be created later by the consumer.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="namespaces are immutable"
	// +optional
	// +kubebuilder:validation:Optional
	Namespaces []Namespaces `json:"namespaces,omitempty"`

	// schemaUpdatePolicy defines how BoundSchemas should be updated when the source schema changes.
	// This is propagated from the APIServiceExportTemplate.
	// Never means schemas are immutable after creation.
	// Always means schemas are updated when the source content changes.
	// +optional
	// +kubebuilder:default="Never"
	// +kubebuilder:validation:Enum=Never;Always
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="schemaUpdatePolicy is immutable"
	SchemaUpdatePolicy SchemaUpdatePolicy `json:"schemaUpdatePolicy,omitempty"`
}

type Namespaces struct {
	// name is the name of the namespace to create on provider side.
	//
	// +required
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

type APIServiceExportRequestResource struct {
	GroupResource `json:",inline"`

	// versions is a list of versions that should be exported. If this is empty
	// a sensible default is chosen by the service provider.
	Versions []string `json:"versions,omitempty"`
}

func (r APIServiceExportRequestResource) ResourceGroupName() string {
	if r.Group == "" {
		r.Group = "core"
	}
	return fmt.Sprintf("%s.%s", r.Resource, r.Group)
}

// Selector is a resource selector that selects objects of a GVR.
// Selectors are ANDed together if multiple are specified.
type Selector struct {
	// NamedResource is a shorthand for selecting a single resource by name and namespace.
	// +optional
	NamedResources []NamedResource `json:"namedResources,omitempty"`

	// LabelSelector is a label selector that selects objects of a GVR.
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// Reference is a reference to the object that contains jsonPath to select objects of a GVR.
	// +optional
	References []SelectorReference `json:"references,omitempty"`
}

// SelectorReference selects objects of a GVR via a reference to another object.
type SelectorReference struct {
	GroupResource `json:",inline"`
	// versions is a list of versions that should be used to fetch the referenced object.
	// If not specified and apiserviceexport contains versions for the same group/resource,
	// those versions are used. Otherwise, references is considered invalid.
	// +optional
	// +kubebuilder:validation:Optional
	Versions []string `json:"versions,omitempty"`
	// JSONPath is a JSONPath expression that selects the name and namespace of the resource from the referenced object.
	// +required
	// +kubebuilder:validation:Required
	JSONPath *JSONPath `json:"jsonPath,omitempty"`
}

// JSONPath specifies the JSONPath expression to select name and namespace of a resource.
type JSONPath struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// NamedResource selects a specific resource by name and namespace.
type NamedResource struct {
	// Name is the name of the resource.
	// Name matches the metadata.name field of the underlying object.
	//
	// +required
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`

	// Namespace represents namespace where an object of the given group/resource may be managed.
	// Namespaces matches against the metadata.namespace field. If not provided, the object is assumed to be cluster-scoped.
	// Namespaces field is ignored for namespaced isolation mode.
	//
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// GroupResource identifies a resource.
type GroupResource struct {
	// group is the name of an API group.
	// For core groups this is the empty string '""'.
	//
	// +kubebuilder:validation:Pattern=`^(|[a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?)$`
	// +kubebuilder:default=""
	Group string `json:"group,omitempty"`

	// resource is the name of the resource.
	// Note: it is worth noting that you can not ask for permissions for resource provided by a CRD
	// not provided by an service binding export.
	//
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]*[a-z0-9]$`
	// +required
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
}

func (r GroupResource) String() string {
	return fmt.Sprintf("%s.%s", r.Resource, r.Group)
}

// PermissionClaim selects objects of a GVR that a service provider may
// request and that a consumer may accept and allow the service provider access to.
type PermissionClaim struct {
	GroupResource `json:",inline"`

	// Selector is a resource selector that selects objects of a GVR.
	// +required
	// +kubebuilder:validation:Required
	Selector Selector `json:"selector"`
}

// Owner is the owner of the resource.
type Owner string

const (
	// OwnerProvider indicates that the resource is owned by the provider.
	OwnerProvider Owner = "provider"
	// OwnerConsumer indicates that the resource is owned by the consumer.
	OwnerConsumer Owner = "consumer"
)

func (o Owner) String() string {
	return string(o)
}

// APIServiceExportRequestPhase describes the phase of a binding request.
type APIServiceExportRequestPhase string

const (
	// APIServiceExportRequestPhasePending indicates that the service binding
	// is in progress.
	APIServiceExportRequestPhasePending APIServiceExportRequestPhase = "Pending"
	// APIServiceExportRequestPhaseFailed indicates that the service binding
	// has failed. It will not resume.
	APIServiceExportRequestPhaseFailed APIServiceExportRequestPhase = "Failed"
	// APIServiceExportRequestPhaseSucceeded indicates that the service binding
	// has succeeded. The corresponding APIServiceExport have been created and
	// are ready.
	APIServiceExportRequestPhaseSucceeded APIServiceExportRequestPhase = "Succeeded"
)

type APIServiceExportRequestStatus struct {
	// phase is the current phase of the binding request. It starts in Pending
	// and transitions to Succeeded or Failed. See the condition for detailed
	// information.
	//
	// +optional
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=Pending
	// +kubebuilder:validation:Enum=Pending;Failed;Succeeded
	Phase APIServiceExportRequestPhase `json:"phase,omitempty"`

	// terminalMessage is a human readable message that describes the reason
	// for the current phase.
	TerminalMessage string `json:"terminalMessage,omitempty"`

	// conditions is a list of conditions that apply to the ClusterBinding. It is
	// updated by the konnector and the service provider.
	Conditions conditionsapi.Conditions `json:"conditions,omitempty"`
}

// APIServiceExportRequestList is the list of APIServiceExportRequest.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIServiceExportRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIServiceExportRequest `json:"items"`
}
