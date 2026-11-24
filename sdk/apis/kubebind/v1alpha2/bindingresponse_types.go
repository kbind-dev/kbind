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
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
)

// BindingResourceResponse is a non-CRUD resource that is returned by the server after
// authentication and resource selection on the service prpvider website. It returns
// a list of requests of possibly different types that kubectl bind has to
// pass to the sub-command kubect-bind-<type>, e.g. kubectl-bind-apiservice for
// APIServiceExportRequest.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
type BindingResourceResponse struct {
	metav1.TypeMeta `json:",inline"`

	// authentication is data specific to the authentication method that was used.
	//
	// +optional
	// +kubebuilder:validation:Optional
	Authentication BindingResponseAuthentication `json:"authentication,omitempty"`

	// kubeconfig is a kubeconfig file that can be used to access the service provider cluster.
	//
	// +required
	// +kubebuilder:validation:Required
	Kubeconfig []byte `json:"kubeconfig"`

	// requests is a list of binding requests of different types, e.g. APIServiceExportRequest.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Requests []runtime.RawExtension `json:"requests"`

	// providerNamespace is the namespace on the service provider cluster where the
	// binding resources (APIServiceExport, BoundSchema, etc.) are created and managed.
	// This is set when the binding is created via the UI flow.
	//
	// +optional
	// +kubebuilder:validation:Optional
	ProviderNamespace string `json:"providerNamespace,omitempty"`

	// providerID is a stable and unique identifier for the provider cluster.
	//
	// +optional
	// +kubebuilder:validation:Optional
	ProviderID string `json:"providerID,omitempty"`

	// bindingName is the confirmed name for this binding, as created on the service
	// provider cluster.
	//
	// +optional
	// +kubebuilder:validation:Optional
	BindingName string `json:"bindingName,omitempty"`
}

// BindingResponseAuthentication is the authentication data specific to the
// authentication method that was used. Exactly one field must be set.
type BindingResponseAuthentication struct {
	// oauth2CodeGrant is the data returned by the OAuth2 code grant flow.
	//
	// +optional
	// +kubebuilder:validation:Optional
	OAuth2CodeGrant *BindingResponseAuthenticationOAuth2CodeGrant `json:"oauth2CodeGrant,omitempty"`
}

// BindingResponseAuthenticationOAuth2CodeGrant contains the authentication data which is passed back to
// the consumer as BindingResponse.Authentication. It is authentication method
// specific.
type BindingResponseAuthenticationOAuth2CodeGrant struct {
	// sessionID is the session ID that was originally passed from the consumer to
	// the service provider. It must be checked to equal the original value.
	SessionID string `json:"sid"`

	// id is the ID of the authenticated user. It is for informational purposes only.
	ID string `json:"id"`
}

// BindableResourcesRequest is sent by the consumer to the service provider
// to indicate which resources the user wants to bind to. It can be sent via
// the HTTP API after authentication, or created directly as a CRD when the
// backend is running without the HTTP API/OIDC flow (frontend disabled mode).
//
// When created as a CRD, a controller will process the request and create the
// necessary APIServiceExport and related resources.
//
// +crd
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Namespaced,categories=kube-bindings
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Template",type="string",JSONPath=`.spec.templateRef.name`,priority=0
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=`.status.phase`,priority=0
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=`.metadata.creationTimestamp`,priority=0
type BindableResourcesRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// spec specifies the binding request details.
	//
	// +required
	// +kubebuilder:validation:Required
	Spec BindableResourcesRequestSpec `json:"spec"`

	// status contains reconciliation information for the binding request.
	// +kubebuilder:default={}
	Status BindableResourcesRequestStatus `json:"status,omitempty"`
}

// BindableResourcesRequestSpec defines the desired state of BindableResourcesRequest.
type BindableResourcesRequestSpec struct {
	// TemplateRef specifies the APIServiceExportTemplate to bind to.
	// +optional
	// +kubebuilder:validation:Optional
	TemplateRef APIServiceExportTemplateRef `json:"templateRef"`

	// ClusterIdentity contains information that uniquely identifies the cluster.
	// This is used when the request is made via the HTTP API after authentication.
	//
	// +required
	// +kubebuilder:validation:Required
	ClusterIdentity ClusterIdentity `json:"clusterIdentity,omitempty"`

	// Author is the identifier of the entity that created this binding request.
	// This is used for audit purposes and to track who initiated the binding.
	//
	// +required
	// +kubebuilder:validation:Required
	Author string `json:"author"`

	// kubeconfigSecretRef is a reference to an existing secret where the binding response
	// will be stored. If specified, the controller will update this secret with the
	// binding response data. If not specified, a new secret will be created with
	// the name "<request-name>-binding-response".
	// +optional
	// +kubebuilder:validation:Optional
	KubeconfigSecretRef *LocalSecretKeyRef `json:"kubeconfigSecretRef,omitempty"`

	// ttlAfterFinished is the TTL after the request has succeeded or failed
	// before it is automatically deleted. If not set, the request will not be
	// automatically deleted. Example values: "1h", "30m", "300s".
	// +optional
	// +kubebuilder:validation:Optional
	TTLAfterFinished *metav1.Duration `json:"ttlAfterFinished,omitempty"`
}

type APIServiceExportTemplateRef struct {
	// name is the name of the APIServiceExportTemplate to bind to.
	//
	// +required
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// BindableResourcesRequestPhase describes the phase of a binding request.
type BindableResourcesRequestPhase string

const (
	// BindableResourcesRequestPhasePending indicates that the binding request is being processed.
	BindableResourcesRequestPhasePending BindableResourcesRequestPhase = "Pending"
	// BindableResourcesRequestPhaseFailed indicates that the binding request has failed.
	BindableResourcesRequestPhaseFailed BindableResourcesRequestPhase = "Failed"
	// BindableResourcesRequestPhaseSucceeded indicates that the binding request has succeeded.
	BindableResourcesRequestPhaseSucceeded BindableResourcesRequestPhase = "Succeeded"
)

// BindableResourcesRequestStatus defines the observed state of BindableResourcesRequest.
type BindableResourcesRequestStatus struct {
	// phase is the current phase of the binding request.
	//
	// +optional
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=Pending
	// +kubebuilder:validation:Enum=Pending;Failed;Succeeded
	Phase BindableResourcesRequestPhase `json:"phase,omitempty"`

	// namespace is the namespace assigned to this binding request on the service provider cluster.
	// This is where the resources for this binding are created and managed.
	//
	// +optional
	// +kubebuilder:validation:Optional
	Namespace string `json:"namespace,omitempty"`

	// kubeconfigSecretRef is a reference to a secret containing the kubeconfig, used
	// to be used by the konnector agent.
	KubeconfigSecretRef *LocalSecretKeyRef `json:"kubeconfigSecretRef,omitempty"`

	// completionTime is the time when the request finished processing (succeeded or failed).
	// Used for TTL-based cleanup.
	// +optional
	// +kubebuilder:validation:Optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// conditions contains the current conditions of the binding request.
	//
	// +optional
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BindableResourcesRequestConditionType defines the condition types for BindableResourcesRequest.
type BindableResourcesRequestConditionType string

const (
	// BindableResourcesRequestConditionReady indicates that the binding response secret is ready.
	BindableResourcesRequestConditionReady BindableResourcesRequestConditionType = "Ready"
)

// BindableResourcesRequestList is the list of BindableResourcesRequest.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type BindableResourcesRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []BindableResourcesRequest `json:"items"`
}

func (r *BindableResourcesRequest) Validate() error {
	if r.Spec.TemplateRef.Name == "" {
		return errors.New("spec.templateRef.name is required")
	}

	if r.Name == "" {
		return errors.New("name is required")
	}

	// Validate DNS name format for the request name
	if errs := validation.IsDNS1123Label(r.Name); len(errs) > 0 {
		return fmt.Errorf("name %q is not a valid DNS label: %s", r.Name, strings.Join(errs, ", "))
	}

	return nil
}
