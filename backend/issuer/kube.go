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

package issuer

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// Scope selects how far the issued RBAC reaches for the granted resources.
type Scope string

const (
	// ScopeCluster grants the exported resources in all namespaces (plus
	// namespace creation), matching the core's identity-preserving sync: the
	// consumer's namespace names are reproduced on the provider. Cross-consumer
	// collisions are handled by the core's ownership markers (first-writer-
	// wins). This is the default.
	ScopeCluster Scope = "Cluster"

	// ScopeNamespace fences the credentials into the boundary namespace only —
	// strict tenancy, with the honest consequence of identity-preserving sync:
	// consumers must place bound objects in a namespace named like the
	// boundary (the issued kubeconfig's context namespace).
	ScopeNamespace Scope = "Namespace"
)

// LabelIdentity marks the boundary namespace with the identity hash that owns
// it (the namespace is shared by all of that identity's Grants).
const LabelIdentity = "iam.kbind.io/identity"

// KubeIssuer is the in-tree Issuer for plain Kubernetes providers.
// Credential mechanism: long-lived secret-based SA token (zero rotation
// friction accepted over security posture; revocation via Grant deletion).
type KubeIssuer struct {
	// Client is a provider-cluster client.
	Client client.Client
	// Scope is the RBAC reach for granted resources. Default ScopeCluster.
	Scope Scope
}

func (i *KubeIssuer) scope() Scope {
	if i.Scope == "" {
		return ScopeCluster
	}
	return i.Scope
}

// clusterRoleName namespaces the cluster-scoped RBAC objects per Grant.
func clusterRoleName(g *iamv1alpha1.Grant) string { return "kbind:grant:" + g.Name }

// tokenSecretName is the SA token Secret for a Grant.
func tokenSecretName(g *iamv1alpha1.Grant) string { return g.Name + "-token" }

// Provision implements Issuer for plain Kubernetes.
func (i *KubeIssuer) Provision(ctx context.Context, g *iamv1alpha1.Grant) (*ProvisionResult, error) {
	ns := BoundaryNamespace(g.Spec.Identity.Subject)

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, namespace, func() error {
		setLabel(&namespace.ObjectMeta, corev1alpha1.LabelManaged, "true")
		setLabel(&namespace.ObjectMeta, LabelIdentity, IdentityHash(g.Spec.Identity.Subject))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ensuring boundary namespace: %w", err)
	}

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, sa, func() error {
		setLabel(&sa.ObjectMeta, iamv1alpha1.LabelGrant, g.Name)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ensuring service account: %w", err)
	}

	// Long-lived secret-based SA token; the token controller populates it.
	token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: tokenSecretName(g)}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, token, func() error {
		setLabel(&token.ObjectMeta, iamv1alpha1.LabelGrant, g.Name)
		if token.Annotations == nil {
			token.Annotations = map[string]string{}
		}
		token.Annotations[corev1.ServiceAccountNameKey] = g.Name
		token.Type = corev1.SecretTypeServiceAccountToken
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ensuring token secret: %w", err)
	}

	if err := i.ensureRBAC(ctx, g, ns); err != nil {
		return nil, err
	}

	return &ProvisionResult{
		Namespace:      ns,
		ServiceAccount: g.Name,
		TokenSecret:    token.Name,
		TokenReady:     len(token.Data[corev1.ServiceAccountTokenKey]) > 0,
	}, nil
}

// ensureRBAC scopes the credentials to exactly the granted APIs (+ declared
// related resources). The split between ClusterRole and Role follows the
// issuance scope; the base ClusterRole (schema discovery + cluster identity)
// and the boundary-namespace Lease permissions (heartbeat) exist in both.
func (i *KubeIssuer) ensureRBAC(ctx context.Context, g *iamv1alpha1.Grant, ns string) error {
	grantRules := i.grantRules(g)

	// Base cluster-wide reads every konnector needs: exported-CRD discovery
	// + schema pull, and the kube-system namespace UID (cluster identity).
	clusterRules := []rbacv1.PolicyRule{
		{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get"}, ResourceNames: []string{"kube-system"}},
	}
	if i.scope() == ScopeCluster {
		clusterRules = append(clusterRules,
			rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "watch", "create"}})
		clusterRules = append(clusterRules, grantRules...)
	}

	cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName(g)}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, cr, func() error {
		setLabel(&cr.ObjectMeta, iamv1alpha1.LabelGrant, g.Name)
		cr.Rules = clusterRules
		return nil
	}); err != nil {
		return fmt.Errorf("ensuring cluster role: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName(g)}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, crb, func() error {
		setLabel(&crb.ObjectMeta, iamv1alpha1.LabelGrant, g.Name)
		crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: cr.Name}
		crb.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: ns, Name: g.Name}}
		return nil
	}); err != nil {
		return fmt.Errorf("ensuring cluster role binding: %w", err)
	}

	// Boundary-namespace Role: the heartbeat Lease home (the issued
	// kubeconfig pins its context namespace here), plus the granted
	// resources when the scope is Namespace.
	roleRules := []rbacv1.PolicyRule{
		{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
	}
	if i.scope() == ScopeNamespace {
		roleRules = append(roleRules, grantRules...)
	}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, role, func() error {
		setLabel(&role.ObjectMeta, iamv1alpha1.LabelGrant, g.Name)
		role.Rules = roleRules
		return nil
	}); err != nil {
		return fmt.Errorf("ensuring role: %w", err)
	}

	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, i.Client, rb, func() error {
		setLabel(&rb.ObjectMeta, iamv1alpha1.LabelGrant, g.Name)
		rb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}
		rb.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: ns, Name: g.Name}}
		return nil
	}); err != nil {
		return fmt.Errorf("ensuring role binding: %w", err)
	}
	return nil
}

// grantRules enumerates the granted APIs and related resources — an explicit,
// reviewable Role, not a wildcard.
func (i *KubeIssuer) grantRules(g *iamv1alpha1.Grant) []rbacv1.PolicyRule {
	rules := make([]rbacv1.PolicyRule, 0, len(g.Spec.APIs)+len(g.Spec.RelatedResources))
	for _, api := range g.Spec.APIs {
		resource, group := SplitAPIName(api.Name)
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{group},
			Resources: []string{resource, resource + "/status"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		})
	}
	for _, rr := range g.Spec.RelatedResources {
		verbs := []string{"get", "list", "watch"} // FromProvider: konnector only reads
		if rr.Direction == corev1alpha1.FromConsumer {
			verbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
		}
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{rr.Group},
			Resources: []string{rr.Resource},
			Verbs:     verbs,
		})
	}
	return rules
}

// Revoke implements Issuer: remove the credentials, keep the boundary
// namespace and any synced objects (destructive cleanup is the reaper's
// explicit opt-in, see DeleteBoundary).
func (i *KubeIssuer) Revoke(ctx context.Context, g *iamv1alpha1.Grant) error {
	ns := BoundaryNamespace(g.Spec.Identity.Subject)
	for _, obj := range []client.Object{
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName(g)}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName(g)}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: tokenSecretName(g)}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}},
	} {
		if err := i.Client.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("revoking %T %s: %w", obj, obj.GetName(), err)
		}
	}
	return nil
}

// DeleteBoundary deletes the Grant's boundary namespace (cascading synced
// objects inside it) — but only if no other live Grant shares the identity.
// Called by the reaper's destructive mode only, never by plain revocation.
func (i *KubeIssuer) DeleteBoundary(ctx context.Context, g *iamv1alpha1.Grant) error {
	var grants iamv1alpha1.GrantList
	if err := i.Client.List(ctx, &grants); err != nil {
		return err
	}
	for idx := range grants.Items {
		other := &grants.Items[idx]
		if other.Name == g.Name || other.DeletionTimestamp != nil {
			continue
		}
		if IdentityHash(other.Spec.Identity.Subject) == IdentityHash(g.Spec.Identity.Subject) {
			return nil // boundary still in use
		}
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: BoundaryNamespace(g.Spec.Identity.Subject)}}
	if err := i.Client.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func setLabel(meta *metav1.ObjectMeta, key, value string) {
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	meta.Labels[key] = value
}
