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

// Condition types used across the core kinds. v2 uses metav1.Condition; the
// konnector sets these with apimachinery's meta.SetStatusCondition helpers.
const (
	// ConditionReady is the top-level summary condition on every core kind.
	ConditionReady = "Ready"

	// Connection conditions.

	// ConditionSecretValid is true when the kubeconfigSecretRef resolves to a
	// usable kubeconfig in the konnector's namespace.
	ConditionSecretValid = "SecretValid"
	// ConditionConnected is true when the konnector can reach the provider and
	// the pinned cluster identity still matches.
	ConditionConnected = "Connected"
	// ConditionSchemaInSync is true when exported schemas are installed on the
	// consumer per the Connection's schema policy.
	ConditionSchemaInSync = "SchemaInSync"

	// Binding conditions.

	// ConditionSynced is true when all listed APIs are installed and their
	// instances are syncing.
	ConditionSynced = "Synced"
	// ConditionConflicts is true when at least one object was skipped due to
	// foreign ownership; see boundAPIs[].conflictCount and per-object conditions.
	ConditionConflicts = "Conflicts"
	// ConditionPermissionDenied is true when an operation was refused by
	// provider RBAC; the binding keeps syncing what it can.
	ConditionPermissionDenied = "PermissionDenied"
)

// Condition reasons.
const (
	// ReasonAsExpected is the success reason for a True Ready/Synced condition.
	ReasonAsExpected = "AsExpected"
	// ReasonPending marks a missing-but-expected dependency (referenced
	// Connection/Secret/CRD not present yet). Not an error; resolves on arrival.
	ReasonPending = "Pending"
	// ReasonSecretNotFound marks an unresolvable kubeconfigSecretRef.
	ReasonSecretNotFound = "SecretNotFound"
	// ReasonClusterIdentityChanged marks a Secret now pointing at a different
	// provider cluster than the one pinned in status.
	ReasonClusterIdentityChanged = "ClusterIdentityChanged"
	// ReasonAPINotExported marks a binding listing an API the Connection does
	// not export to these credentials.
	ReasonAPINotExported = "APINotExported"
	// ReasonForeignObjectExists marks a conflict with an un-owned target object.
	ReasonForeignObjectExists = "ForeignObjectExists"
	// ReasonOwnedByAnother marks a conflict with an object owned by a different
	// binding/consumer.
	ReasonOwnedByAnother = "OwnedByAnother"
	// ReasonForbidden marks an operation refused by provider RBAC.
	ReasonForbidden = "Forbidden"
)
