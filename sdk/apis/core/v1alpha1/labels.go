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

const (
	// LabelExported marks a provider CRD as exported to consumers. The
	// konnector's discovery (schema.source: CRD) lists CRDs carrying this label.
	// On CRD-less providers the logical-cluster boundary is the export boundary
	// instead, and no label is needed.
	LabelExported = "core.kbind.io/exported"

	// LabelManaged marks an object (or namespace) written by the konnector on
	// the provider.
	LabelManaged = "core.kbind.io/managed"

	// AnnotationConnection records, on a pulled consumer CRD, the name of the
	// Connection whose provider it was pulled from. The sync engine uses it to
	// pin the provider cluster for that API.
	AnnotationConnection = "core.kbind.io/connection"

	// AnnotationConsumerClusterUID records the consumer cluster identity on a
	// synced provider object (ownership marker).
	AnnotationConsumerClusterUID = "core.kbind.io/consumer-cluster-uid"

	// AnnotationConsumerObjectUID records the source consumer object UID on a
	// synced provider object (ownership marker).
	AnnotationConsumerObjectUID = "core.kbind.io/consumer-object-uid"

	// AnnotationRelatedBinding records, on a synced related Secret/ConfigMap, the
	// UID of the binding that pulled it in, so it can be GC'd when it stops
	// matching the selector or the binding is removed.
	AnnotationRelatedBinding = "core.kbind.io/related-binding"

	// AnnotationConflict marks a consumer instance the konnector refused to sync
	// because the provider target is owned by another binding/consumer. The
	// value is the conflict reason. Bindings count annotated instances to report
	// conflictCount. Survives status-schema pruning (it is metadata, not status).
	AnnotationConflict = "core.kbind.io/conflict"

	// AnnotationDeletionPolicy on a consumer instance controls what happens to
	// its provider copy when the consumer object is deleted or unbound. The only
	// non-default value is "Orphan": release the finalizer without deleting the
	// provider copy (keep the managed object on the provider).
	AnnotationDeletionPolicy = "core.kbind.io/deletion-policy"

	// DeletionPolicyOrphan keeps the provider copy when the consumer object is
	// deleted or unbound.
	DeletionPolicyOrphan = "Orphan"

	// FinalizerSyncer blocks consumer-object deletion until the provider copy
	// has been removed.
	FinalizerSyncer = "core.kbind.io/syncer"

	// FinalizerCleanup blocks deletion of a Connection or Binding until the
	// konnector has unwound what it created: provider copies of synced
	// instances, pulled CRDs, and instance finalizers. It is also placed on a
	// Connection's referenced Secret so the credential survives long enough to
	// reach the provider during teardown.
	FinalizerCleanup = "core.kbind.io/cleanup"
)
