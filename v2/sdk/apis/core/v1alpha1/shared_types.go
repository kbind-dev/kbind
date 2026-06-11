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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretKeyRef references a key within a Secret. The Secret must live in the
// konnector's designated namespace; a cluster-scoped Connection may not reach
// into arbitrary namespaces (privilege-escalation guard, see proposal F1).
type SecretKeyRef struct {
	// namespace of the Secret. Must be the konnector's designated namespace.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// name of the Secret.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// key within the Secret holding the kubeconfig.
	//
	// +kubebuilder:default=kubeconfig
	Key string `json:"key,omitempty"`
}

// ConnectionRef references a Connection by name. Connection is cluster-scoped,
// so no namespace is needed.
type ConnectionRef struct {
	// name of the Connection.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// APIRef identifies an exported API by its CRD name on the provider,
// i.e. "<plural>.<group>" (for example "mangodbs.mangodb.io"). Under the
// OpenAPI schema source there is no CRD object behind the name; it is still
// just resource + group.
type APIRef struct {
	// name is the CRD name of the exported API, "<plural>.<group>".
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// BindingSpec is the spec shared by ClusterBinding and Binding. The only
// difference between the two kinds is scope (cluster-wide vs. one namespace),
// which is expressed by the kind, not by a field.
type BindingSpec struct {
	// connectionRef points at the Connection that provides the provider link
	// and credentials for the listed APIs.
	//
	// +required
	// +kubebuilder:validation:Required
	ConnectionRef ConnectionRef `json:"connectionRef"`

	// apis lists one or more exported CRDs to sync, by CRD name on the provider.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	APIs []APIRef `json:"apis"`

	// conflictPolicy controls what happens when a target object already exists.
	// Fail (default) leaves a foreign object untouched and records a conflict;
	// Adopt takes ownership of an un-owned object. Adopt never steals an object
	// already carrying another binding's/consumer's markers.
	//
	// +kubebuilder:validation:Enum=Fail;Adopt
	// +kubebuilder:default=Fail
	ConflictPolicy ConflictPolicy `json:"conflictPolicy,omitempty"`

	// relatedResources are Secrets/ConfigMaps synced alongside instances of the
	// bound APIs. Not yet synced in the alpha POC.
	//
	// +optional
	// +listType=atomic
	RelatedResources []RelatedResource `json:"relatedResources,omitempty"`
}

// ConflictPolicy is the behavior for pre-existing target objects.
type ConflictPolicy string

const (
	// ConflictPolicyFail leaves a foreign target object untouched and records
	// the collision as a conflict. This is the default.
	ConflictPolicyFail ConflictPolicy = "Fail"

	// ConflictPolicyAdopt takes ownership of an un-owned target object.
	ConflictPolicyAdopt ConflictPolicy = "Adopt"
)

// RelatedResource selects auxiliary objects (Secrets/ConfigMaps) to sync
// alongside the bound instances.
type RelatedResource struct {
	// group of the related resource. Empty string for the core group.
	//
	// +kubebuilder:default=""
	Group string `json:"group"`

	// resource is the plural resource name. Only "secrets" and "configmaps"
	// are permitted in core.
	//
	// +kubebuilder:validation:Enum=secrets;configmaps
	// +required
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`

	// direction the related resource flows.
	//
	// +kubebuilder:validation:Enum=FromProvider;FromConsumer
	// +required
	// +kubebuilder:validation:Required
	Direction SyncDirection `json:"direction"`

	// selector restricts which objects are synced. Only labelSelector and
	// named selectors are supported in core (no JSONPath reference-following).
	//
	// +optional
	Selector *RelatedResourceSelector `json:"selector,omitempty"`
}

// SyncDirection is the direction a related resource is synced.
type SyncDirection string

const (
	// FromProvider syncs the related resource provider -> consumer.
	FromProvider SyncDirection = "FromProvider"
	// FromConsumer syncs the related resource consumer -> provider.
	FromConsumer SyncDirection = "FromConsumer"
)

// RelatedResourceSelector restricts which related objects are synced.
type RelatedResourceSelector struct {
	// labelSelector selects related objects by label.
	//
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// names selects related objects by exact name.
	//
	// +optional
	// +listType=set
	Names []string `json:"names,omitempty"`
}

// BoundAPI is the per-API observed state recorded on a binding's status.
type BoundAPI struct {
	// name is the CRD name of the bound API, "<plural>.<group>".
	Name string `json:"name"`

	// crdHash is a hash of the schema currently applied on the consumer for
	// this API. Empty until the schema has been installed.
	//
	// +optional
	CRDHash string `json:"crdHash,omitempty"`

	// conflictCount is the number of objects skipped due to foreign ownership.
	// Per-object detail lives on each object's own condition, not here.
	//
	// +optional
	ConflictCount int32 `json:"conflictCount,omitempty"`
}

// ExportedAPI describes one API the provider exports to these credentials.
type ExportedAPI struct {
	// name is the CRD name of the exported API, "<plural>.<group>".
	Name string `json:"name"`

	// group of the exported API.
	Group string `json:"group"`

	// resource is the plural resource name of the exported API.
	Resource string `json:"resource"`

	// scope is whether the API is Namespaced or Cluster scoped.
	Scope apiextensionsv1.ResourceScope `json:"scope"`

	// versions are the served versions of the exported API.
	//
	// +listType=set
	Versions []string `json:"versions"`
}
