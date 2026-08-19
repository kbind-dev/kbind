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

const (
	// LabelGrant marks provider objects (namespace, ServiceAccount, RBAC,
	// token Secret) provisioned by the issuer for a Grant; the value is the
	// Grant's name.
	LabelGrant = "iam.kbind.io/grant"

	// AnnotationPickupSHA256 on a Grant holds the SHA-256 of the one-time
	// bundle pickup token's random part. The gateway stamps it at bind time
	// and removes it (with optimistic concurrency) on pickup, so single-use
	// is enforced by the API server and the gateway keeps zero state.
	AnnotationPickupSHA256 = "iam.kbind.io/pickup-sha256"

	// AnnotationPickupExpiry on a Grant holds the RFC 3339 expiry of the
	// pickup token. Applies to the pickup URL only — the SA token inside a
	// picked-up bundle stays valid until the Grant is revoked.
	AnnotationPickupExpiry = "iam.kbind.io/pickup-expiry"

	// AnnotationStaleSince on a Grant is stamped by the reaper when the
	// consumer's heartbeat Lease has been expired (or absent) beyond the
	// configured TTL. Cleared when the heartbeat resumes.
	AnnotationStaleSince = "iam.kbind.io/stale-since"

	// FinalizerCleanup blocks Grant deletion until the issuer has unwound the
	// provisioned namespace, ServiceAccount, RBAC and token Secret.
	FinalizerCleanup = "iam.kbind.io/cleanup"
)

// Condition types and reasons for iam kinds.
const (
	// ConditionReady is true when the credentials are provisioned and the
	// token Secret is populated.
	ConditionReady = "Ready"

	// ConditionStale is true when the reaper considers the consumer behind
	// this Grant gone (heartbeat Lease expired beyond TTL).
	ConditionStale = "Stale"

	// ReasonAsExpected is the success reason.
	ReasonAsExpected = "AsExpected"
	// ReasonPending marks provisioning still in progress (e.g. token not yet
	// populated by the token controller).
	ReasonPending = "Pending"
	// ReasonLeaseExpired marks a Grant whose consumer heartbeat Lease expired.
	ReasonLeaseExpired = "LeaseExpired"
	// ReasonNoLease marks a Grant whose boundary namespace never saw a
	// heartbeat Lease.
	ReasonNoLease = "NoLease"
)
