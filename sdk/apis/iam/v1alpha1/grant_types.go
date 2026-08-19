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

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

// Grant records that an identity was issued credentials for an export. The
// gateway creates it with the export's API list and defaults resolved in
// (issuance is a stable record even if the catalog entry changes later); the
// issuer controller provisions the tenancy boundary, ServiceAccount, RBAC and
// token from the spec and reports the artifacts in status. Deleting the Grant
// revokes: the issuer's cleanup finalizer unwinds everything it provisioned.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=kbind
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Subject",type=string,JSONPath=`.spec.identity.subject`
// +kubebuilder:printcolumn:name="Export",type=string,JSONPath=`.spec.exportName`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Grant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required
	Spec GrantSpec `json:"spec"`

	// +optional
	Status GrantStatus `json:"status,omitempty"`
}

// GrantSpec is the issuance request/record.
type GrantSpec struct {
	// identity is who the credentials were issued to.
	//
	// +required
	// +kubebuilder:validation:Required
	Identity Identity `json:"identity"`

	// exportName records which catalog Export this Grant was issued for.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ExportName string `json:"exportName"`

	// apis is the export's API list resolved at issuance time, by CRD name on
	// the provider ("<plural>.<group>"). The issuer scopes RBAC to exactly
	// these.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	APIs []corev1alpha1.APIRef `json:"apis"`

	// conflictPolicy is the export's default conflict policy resolved at
	// issuance time, copied into the generated bundle's ClusterBinding.
	//
	// +optional
	// +kubebuilder:validation:Enum=Fail;Adopt
	ConflictPolicy corev1alpha1.ConflictPolicy `json:"conflictPolicy,omitempty"`

	// relatedResources are the export's related-resource selectors resolved at
	// issuance time. They flow into the bundle's ClusterBinding and widen the
	// issued RBAC (secrets/configmaps in the declared direction).
	//
	// +optional
	// +listType=atomic
	RelatedResources []corev1alpha1.RelatedResource `json:"relatedResources,omitempty"`
}

// Identity is the consumer identity credentials were issued to. The tenancy
// key is issuer+"/"+subject, so the same human gets the same boundary on
// re-bind.
type Identity struct {
	// subject is the stable identity key, "<issuer>#<subject>" for OIDC.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Subject string `json:"subject"`

	// displayName is a human-facing name (e.g. email), informational only.
	//
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// groups the identity carried at issuance, informational only.
	//
	// +optional
	// +listType=atomic
	Groups []string `json:"groups,omitempty"`
}

// GrantStatus reports what the issuer provisioned.
type GrantStatus struct {
	// namespace is the per-consumer tenancy boundary on the provider.
	//
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// serviceAccount is the name of the issued ServiceAccount (in namespace).
	//
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// tokenSecret is the name of the long-lived SA token Secret (in
	// namespace). The gateway reads it to assemble the bundle's kubeconfig.
	//
	// +optional
	TokenSecret string `json:"tokenSecret,omitempty"`

	// conditions: Ready (credentials provisioned and token populated).
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GrantList contains a list of Grant.
//
// +kubebuilder:object:root=true
type GrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Grant `json:"items"`
}
