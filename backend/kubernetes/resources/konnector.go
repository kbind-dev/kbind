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

package resources

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

const (
	KonnectorNamespace          = "kube-bind"
	KonnectorServiceAccountName = "konnector"
	KonnectorClusterRoleName    = "kube-bind-konnector"
	KonnectorDeploymentName     = "konnector"
	KonnectorReplicas           = 2
	KonnectorHealthzPort        = 8090
)

// KonnectorManifests holds typed Kubernetes objects for deploying the konnector agent.
type KonnectorManifests struct {
	Namespace          *corev1.Namespace
	ServiceAccount     *corev1.ServiceAccount
	ClusterRole        *rbacv1.ClusterRole
	ClusterRoleBinding *rbacv1.ClusterRoleBinding
	Deployment         *appsv1.Deployment
}

// NewKonnectorManifests returns the full set of Kubernetes objects needed to deploy
// the konnector agent to a consumer cluster.
func NewKonnectorManifests(konnectorImage string, hostAliases []corev1.HostAlias) *KonnectorManifests {
	replicas := int32(KonnectorReplicas)
	httpPort := intstr.FromInt(KonnectorHealthzPort)

	return &KonnectorManifests{
		Namespace: &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: KonnectorNamespace},
		},
		ServiceAccount: &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      KonnectorServiceAccountName,
				Namespace: KonnectorNamespace,
			},
		},
		// The konnector dynamically manages CRDs and syncs arbitrary resource types.
		// Wildcard permissions are no longer used here; instead, the kubectl bind
		// CLI dynamically creates RBAC for the bound custom resources.
		ClusterRole: &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: KonnectorClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"namespaces", "secrets", "events", "serviceaccounts", "configmaps"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				},
				{
					APIGroups: []string{"kube-bind.io"},
					Resources: []string{"apiservicebindings", "apiservicebindings/status", "clusterbindings", "clusterbindings/status", "servicebindings", "servicebindings/status", "serviceexports", "serviceexports/status"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				},
				{
					APIGroups: []string{"apiextensions.k8s.io"},
					Resources: []string{"customresourcedefinitions"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				},
				{
					APIGroups: []string{"coordination.k8s.io"},
					Resources: []string{"leases"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				},
			},
		},
		ClusterRoleBinding: &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: KonnectorClusterRoleName,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     KonnectorClusterRoleName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      KonnectorServiceAccountName,
					Namespace: KonnectorNamespace,
				},
			},
		},
		Deployment: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      KonnectorDeploymentName,
				Namespace: KonnectorNamespace,
				Labels:    map[string]string{"app": "konnector"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "konnector"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "konnector"},
					},
					Spec: corev1.PodSpec{
						RestartPolicy:      corev1.RestartPolicyAlways,
						ServiceAccountName: KonnectorServiceAccountName,
						HostAliases:        hostAliases,
						Containers: []corev1.Container{
							{
								Name:  "konnector",
								Image: konnectorImage,
								Env: []corev1.EnvVar{
									{
										Name: "POD_NAME",
										ValueFrom: &corev1.EnvVarSource{
											FieldRef: &corev1.ObjectFieldSelector{
												FieldPath: "metadata.name",
											},
										},
									},
									{
										Name: "POD_NAMESPACE",
										ValueFrom: &corev1.EnvVarSource{
											FieldRef: &corev1.ObjectFieldSelector{
												FieldPath: "metadata.namespace",
											},
										},
									},
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/healthz",
											Port: httpPort,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// Objects returns all konnector manifests as a slice of runtime.Object for serialization.
func (m *KonnectorManifests) Objects() []runtime.Object {
	return []runtime.Object{
		m.Namespace,
		m.ServiceAccount,
		m.ClusterRole,
		m.ClusterRoleBinding,
		m.Deployment,
	}
}

// AddKonnectorSchemes registers the schemes needed to serialize konnector manifests.
func AddKonnectorSchemes(s *runtime.Scheme) {
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(appsv1.AddToScheme(s))
	utilruntime.Must(rbacv1.AddToScheme(s))
}
