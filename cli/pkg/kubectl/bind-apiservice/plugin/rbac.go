package plugin

import (
	"context"
	"fmt"

	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func ensureKonnectorDynamicRBAC(ctx context.Context, config *rest.Config, binding *kubebindv1alpha2.APIServiceBinding, request *kubebindv1alpha2.APIServiceExportRequest) error {
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	roleName := fmt.Sprintf("kube-bind-konnector-%s", binding.Name)
	ownerRef := metav1.OwnerReference{
		APIVersion: kubebindv1alpha2.SchemeGroupVersion.String(),
		Kind:       "APIServiceBinding",
		Name:       binding.Name,
		UID:        binding.UID,
	}

	var rules []rbacv1.PolicyRule
	for _, res := range request.Spec.Resources {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{res.Group},
			Resources: []string{res.Resource, res.Resource + "/status"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		})
	}
	for _, claim := range request.Spec.PermissionClaims {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{claim.Group},
			Resources: []string{claim.Resource, claim.Resource + "/status"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		})
	}

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Rules: rules,
	}

	_, err = kubeClient.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, err := kubeClient.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			existing.Rules = rules
			existing.OwnerReferences = []metav1.OwnerReference{ownerRef}
			_, err = kubeClient.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      KonnectorServiceAccount,
				Namespace: KonnectorNamespace,
			},
		},
	}

	_, err = kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, err := kubeClient.RbacV1().ClusterRoleBindings().Get(ctx, roleName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			existing.RoleRef = clusterRoleBinding.RoleRef
			existing.Subjects = clusterRoleBinding.Subjects
			existing.OwnerReferences = []metav1.OwnerReference{ownerRef}
			_, err = kubeClient.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}
