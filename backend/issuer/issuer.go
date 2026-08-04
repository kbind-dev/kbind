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

// Package issuer provisions per-consumer credentials on the provider cluster:
// a tenancy boundary (namespace), a ServiceAccount with RBAC scoped to the
// granted APIs, and a long-lived SA token. The Grant (iam.kbind.io) is the
// issuance record; deleting it revokes. The in-tree implementation is plain
// Kubernetes only — a kcp issuer implements the same interface out of tree.
package issuer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// Issuer is the provisioning boundary of the service layer: make a Grant's
// credentials exist, and unwind them on revocation.
type Issuer interface {
	// Provision ensures the Grant's boundary, ServiceAccount, RBAC and token
	// exist, and reports what was provisioned. Idempotent.
	Provision(ctx context.Context, grant *iamv1alpha1.Grant) (*ProvisionResult, error)
	// Revoke removes the Grant's credentials (ServiceAccount, RBAC, token).
	// It does NOT delete the boundary namespace or synced objects — that
	// destructive step belongs to the reaper's explicit opt-in.
	Revoke(ctx context.Context, grant *iamv1alpha1.Grant) error
}

// ProvisionResult reports the artifacts of an issuance.
type ProvisionResult struct {
	// Namespace is the per-consumer tenancy boundary.
	Namespace string
	// ServiceAccount is the issued ServiceAccount's name (in Namespace).
	ServiceAccount string
	// TokenSecret is the SA token Secret's name (in Namespace).
	TokenSecret string
	// TokenReady is true once the token controller populated the Secret.
	TokenReady bool
}

// IdentityHash is the stable tenancy key for a subject ("<issuer>#<sub>"):
// same human, same boundary, on every re-bind.
func IdentityHash(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])[:10]
}

// BoundaryNamespace is the per-identity provider namespace.
func BoundaryNamespace(subject string) string {
	return "kbind-" + IdentityHash(subject)
}

// GrantName is the deterministic Grant name for (export, identity), so a
// re-bind of the same export by the same human updates the existing record.
func GrantName(exportName, subject string) string {
	return exportName + "-" + IdentityHash(subject)
}

// SplitAPIName splits a CRD name "<plural>.<group>" into resource and group
// (group may be empty for core-group names, which do not occur in practice).
func SplitAPIName(name string) (resource, group string) {
	resource, group, _ = strings.Cut(name, ".")
	return resource, group
}
