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

package plugin

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

const (
	KonnectorNamespace          = "kube-bind"
	KonnectorAppName            = "konnector"
	KonnectorServiceAccount     = "konnector"
	KonnectorClusterRole        = "kube-bind-konnector"
	KonnectorClusterRoleBinding = "kube-bind-konnector"
)

func bootstrapKonnector(
	ctx context.Context,
	discoveryClient discovery.DiscoveryInterface,
	dynamicClient dynamic.Interface,
	image string,
	hostAliases []corev1.HostAlias,
) error {
	resources := getAllKonnectorResources(image, hostAliases)

	for _, resource := range resources {
		if err := applyResource(ctx, dynamicClient, discoveryClient, resource); err != nil {
			return err
		}
	}

	return nil
}

// getAllKonnectorResources returns all konnector resources needed for deployment
func getAllKonnectorResources(image string, hostAliases []corev1.HostAlias) []interface{} {
	return []interface{}{
		getKonnectorNamespace(),
		getKonnectorServiceAccount(),
		getKonnectorClusterRole(),
		getKonnectorClusterRoleBinding(),
		getKonnectorDeployment(image, hostAliases),
	}
}

// getKonnectorNamespace returns the namespace for konnector deployment
func getKonnectorNamespace() *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: KonnectorNamespace,
		},
	}
}

// getKonnectorServiceAccount returns the service account for konnector
func getKonnectorServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      KonnectorServiceAccount,
			Namespace: KonnectorNamespace,
		},
	}
}

// getKonnectorClusterRole returns the cluster role for konnector
func getKonnectorClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: KonnectorClusterRole,
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
	}
}

// getKonnectorClusterRoleBinding returns the cluster role binding for konnector
func getKonnectorClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: KonnectorClusterRoleBinding,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     KonnectorClusterRole,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      KonnectorServiceAccount,
				Namespace: KonnectorNamespace,
			},
		},
	}
}

// getKonnectorDeployment returns the deployment for konnector
func getKonnectorDeployment(image string, hostAliases []corev1.HostAlias) *appsv1.Deployment {
	replicas := int32(2)

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      KonnectorAppName,
			Namespace: KonnectorNamespace,
			Labels: map[string]string{
				"app": KonnectorAppName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": KonnectorAppName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": KonnectorAppName,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyAlways,
					ServiceAccountName: KonnectorServiceAccount,
					HostAliases:        hostAliases,
					Containers: []corev1.Container{
						{
							Name:  KonnectorAppName,
							Image: image,
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
										Port: intstr.FromInt(8090),
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

func applyResource(ctx context.Context, dynamicClient dynamic.Interface, discoveryClient discovery.DiscoveryInterface, obj interface{}) error {
	runtimeObj, ok := obj.(runtime.Object)
	if !ok {
		return nil
	}

	// Convert to unstructured
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(runtimeObj)
	if err != nil {
		return err
	}

	// Extract metadata
	metadata, ok := unstructuredObj["metadata"].(map[string]interface{})
	if !ok {
		return nil
	}

	name, _ := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)

	// Get GVK information
	gvk := runtimeObj.GetObjectKind().GroupVersionKind()

	// Get GVR from discovery
	gvr := getGVRFromGVK(gvk)

	// Apply the resource
	var resourceClient dynamic.ResourceInterface
	if namespace != "" {
		resourceClient = dynamicClient.Resource(gvr).Namespace(namespace)
	} else {
		resourceClient = dynamicClient.Resource(gvr)
	}

	// Try to get existing resource
	_, err = resourceClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Create new resource
			_, err = resourceClient.Create(ctx, &unstructured.Unstructured{Object: unstructuredObj}, metav1.CreateOptions{})
			return err
		}
		return err
	}

	// Update existing resource
	_, err = resourceClient.Update(ctx, &unstructured.Unstructured{Object: unstructuredObj}, metav1.UpdateOptions{})
	return err
}

func getGVRFromGVK(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	gvkMap := map[schema.GroupVersionKind]schema.GroupVersionResource{
		{Group: "", Version: "v1", Kind: "Namespace"}: {
			Group:    "",
			Version:  "v1",
			Resource: "namespaces",
		},
		{Group: "", Version: "v1", Kind: "ServiceAccount"}: {
			Group:    "",
			Version:  "v1",
			Resource: "serviceaccounts",
		},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}: {
			Group:    "rbac.authorization.k8s.io",
			Version:  "v1",
			Resource: "clusterroles",
		},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"}: {
			Group:    "rbac.authorization.k8s.io",
			Version:  "v1",
			Resource: "clusterrolebindings",
		},
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {
			Group:    "apps",
			Version:  "v1",
			Resource: "deployments",
		},
	}
	if gvr, found := gvkMap[gvk]; found {
		return gvr
	}

	return schema.GroupVersionResource{}
}
