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

package e2e

import (
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	kcpboostrapdeploy "github.com/kube-bind/kube-bind/contrib/kcp/deploy"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	"github.com/kube-bind/kube-bind/test/e2e/framework"
)

// TestKCP3TierClusterScope tests the 3-tier kcp flow with cluster-scoped resources.
func TestKCP3TierClusterScope(t *testing.T) {
	t.Parallel()
	testKcp3TierIntegration(t, "3cc", kubebindv1alpha2.ClusterScope)
}

// TestKCP3TierNamespacedScope tests the 3-tier kcp flow with namespaced resources.
func TestKCP3TierNamespacedScope(t *testing.T) {
	t.Parallel()
	testKcp3TierIntegration(t, "3nc", kubebindv1alpha2.NamespacedScope)
}

// testKcp3TierIntegration tests the 3-tier kcp flow:
//
//	:root:provider   — owns the APIResourceSchema + APIExport (source of truth)
//	      |
//	      |  (APIBinding: :root:backend binds from :root:provider)
//	      v
//	:root:backend    — binds resources from provider; apibindingtemplate controller
//	                   auto-creates APIServiceExportTemplates; apiresourceschema
//	                   controller copies schemas with kube-bind.io/exported=true label
//	      |
//	      |  (kube-bind serves the binding API)
//	      v
//	  consumer       — binds resources via kube-bind
func testKcp3TierIntegration(t *testing.T, name string, scope kubebindv1alpha2.InformerScope) {
	t.Helper()
	t.Logf("Testing 3-tier kcp integration with informer scope %s", scope)

	// kcp bootstrap (creates :root:kube-bind workspace with APIExport etc.)
	// Note: no StartDex needed — the backend uses embedded OIDC (mockoidc) which auto-approves.
	testRootCfg, kbindWsPath := bootstrapKCP(t, framework.ClientConfig(t))
	testRootPath, _ := kbindWsPath.Parent()

	suffix := framework.RandomString(4)

	cfg := framework.ClientConfig(t)
	cfg.Host = strings.Split(cfg.Host, "/clusters/")[0]
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to create kcp client")

	// --- Provider workspace ---
	t.Log("Create provider workspace")
	providerWsName := fmt.Sprintf("%s-provider-%s", name, suffix)
	providerWsPath := testRootPath.Join(providerWsName)
	providerCfg, _ := framework.NewWorkspace(t, testRootCfg, framework.WithStaticName(providerWsName))

	t.Log("Applying APIResourceSchema and APIExport to provider workspace")
	// Apply the cowboys APIResourceSchema and a cowboys-only APIExport.
	providerFiles := []string{
		"examples/apiresourceschema-cowboys.yaml",
		"examples/apiresourceschema-sheriffs.yaml",
	}
	for _, f := range providerFiles {
		data, err := kcpboostrapdeploy.Examples.ReadFile(f)
		require.NoError(t, err, "failed to read provider file %s", f)
		framework.ApplyManifest(t, providerCfg, data)
	}
	// Apply the combined APIExport (cowboys + sheriffs).
	data, err := kcpboostrapdeploy.Examples.ReadFile("examples/apiexport.yaml")
	require.NoError(t, err)
	framework.ApplyManifest(t, providerCfg, data)

	// --- Consumer workspace ---
	t.Log("Create consumer workspace")
	consumerWsName := fmt.Sprintf("%s-consumer-%s", name, suffix)
	consumerCfg, consumerKubeconfigPath := framework.NewWorkspace(t, testRootCfg, framework.WithStaticName(consumerWsName))

	t.Log("Start konnector for consumer workspace")
	framework.StartKonnector(t, consumerCfg, "--kubeconfig="+consumerKubeconfigPath)

	// --- Backend (kube-bind process + backend workspace) ---
	backendAddr := bootstrapBackend(t, framework.ClientConfig(t), scope, kbindWsPath)

	// --- Backend workspace ---
	// The backend workspace binds both kube-bind.io and the provider's cowboys APIExport.
	// The apibindingtemplate and apiresourceschema controllers will automatically:
	// 1. Create APIServiceExportTemplates from the cowboys APIBinding
	// 2. Copy APIResourceSchemas with kube-bind.io/exported=true label
	t.Log("Create backend workspace")
	backendWsName := fmt.Sprintf("%s-backend-%s", name, suffix)
	backendWsPath := testRootPath.Join(backendWsName)
	backendCfg, _ := framework.NewWorkspace(t, testRootCfg, framework.WithStaticName(backendWsName))

	// Grant the embedded OIDC user permission to bind kube-bind.io resources in
	// the backend workspace. This is required for the backend's bind
	// SubjectAccessReview during the login flow.
	rbacData, err := kcpboostrapdeploy.Examples.ReadFile("examples/rbac-embedded-user.yaml")
	require.NoError(t, err, "failed to read embedded user RBAC")
	framework.ApplyManifest(t, backendCfg, rbacData)

	t.Log("Bind kube-bind.io APIExport to backend workspace")
	createApiBinding(t,
		kcpClusterClient,
		backendWsPath,
		generateApiBinding(t, kbindWsPath, "kube-bind.io", "kube-bind.io",
			"clusterrolebindings.rbac.authorization.k8s.io",
			"clusterroles.rbac.authorization.k8s.io",
			"customresourcedefinitions.apiextensions.k8s.io",
			"serviceaccounts.core",
			"configmaps.core",
			"secrets.core",
			"namespaces.core",
			"roles.rbac.authorization.k8s.io",
			"rolebindings.rbac.authorization.k8s.io",
			"subjectaccessreviews.authorization.k8s.io",
			"apiresourceschemas.apis.kcp.io",
			"apibindings.apis.kcp.io",
		),
	)

	t.Log("Bind provider's cowboys-stable APIExport to backend workspace")
	createApiBinding(t,
		kcpClusterClient,
		backendWsPath,
		generateApiBinding(t, providerWsPath, "cowboys-stable", "cowboys-stable"),
	)

	// --- Wait for dynamic template creation ---
	// The apibindingtemplate controller creates separate templates per scope:
	// cowboys-stable-namespaced (for cowboys) and cowboys-stable-cluster (for sheriffs).
	t.Log("Waiting for APIServiceExportTemplates to be auto-created by apibindingtemplate controller")
	templateGVR := schema.GroupVersionResource{Group: "kube-bind.io", Version: "v1alpha2", Resource: "apiserviceexporttemplates"}
	templateDynClient := framework.DynamicClient(t, backendCfg).Resource(templateGVR)
	expectedTemplates := []string{"cowboys-stable-namespaced", "cowboys-stable-cluster"}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		templates, err := templateDynClient.List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("Error listing templates: %v", err)
		}
		found := map[string]bool{}
		for _, tmpl := range templates.Items {
			found[tmpl.GetName()] = true
		}
		for _, expected := range expectedTemplates {
			if !found[expected] {
				return false, fmt.Sprintf("Template %q not found, got %v", expected, found)
			}
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Second)

	// --- Wait for APIResourceSchema copy ---
	// The apiresourceschema controller should copy the bound schemas with the
	// kube-bind.io/exported=true label.
	t.Log("Waiting for APIResourceSchema copies with exported label")
	waitForExportedSchemas(t, backendCfg)

	// --- Get logical cluster ID for login ---
	t.Log("Get logical cluster of backend workspace")
	backendCluster, err := kcpClusterClient.Cluster(backendWsPath).CoreV1alpha1().LogicalClusters().Get(t.Context(), "cluster", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, backendCluster.Status.URL, "backend cluster URL is empty")

	backendClusterSplit := strings.Split(backendCluster.Status.URL, "/")
	require.GreaterOrEqual(t, len(backendClusterSplit), 2, "Unexpected URL format: %s", backendCluster.Status.URL)
	require.Equal(t, "clusters", backendClusterSplit[len(backendClusterSplit)-2], "Unexpected URL format: %s", backendCluster.Status.URL)
	backendClusterID := backendClusterSplit[len(backendClusterSplit)-1]
	require.NotEmpty(t, backendClusterID, "Retrieved cluster id is empty")

	// Wait for the backend to engage the backend workspace before logging in,
	// as the bind SubjectAccessReview during login depends on it.
	t.Log("Wait for backend to engage backend workspace")
	waitForClusterEngaged(t, backendCfg)

	// --- Binding ---
	var templateRef, kind, resource string
	switch scope {
	case kubebindv1alpha2.ClusterScope:
		kind = "Sheriff"
		resource = "sheriffs"
		templateRef = "cowboys-stable-cluster"
	case kubebindv1alpha2.NamespacedScope:
		kind = "Cowboy"
		resource = "cowboys"
		templateRef = "cowboys-stable-namespaced"
	default:
		require.Fail(t, "unhandled scope %q", scope)
	}

	kubeBindConfig := path.Join(framework.WorkDir(t), "kube-bind-config-kcp-3tier.yaml")

	iostreams, _, _, _ := genericclioptions.NewTestIOStreams()
	authURLDryRunCh := make(chan string, 1)
	go framework.SimulateBrowser(t, authURLDryRunCh)
	framework.Login(t, iostreams, authURLDryRunCh, kubeBindConfig, fmt.Sprintf("http://%s/api/exports", backendAddr), backendClusterID)

	t.Logf("Performing binding using template %s", templateRef)
	performBinding(t, consumerCfg, templateRef, resource, kubeBindConfig)

	// --- Resource sync ---
	t.Log("Testing resource creation and synchronization...")
	testKCP3TierResourceSync(t, consumerCfg, backendCfg, scope, kind, resource)
}

// waitForExportedSchemas waits for APIResourceSchemas with the kube-bind.io/exported=true
// label to appear in the backend workspace. These are created by the apiresourceschema controller.
func waitForExportedSchemas(t *testing.T, backendCfg *rest.Config) {
	t.Helper()

	schemaGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	dynClient := framework.DynamicClient(t, backendCfg).Resource(schemaGVR)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		schemas, err := dynClient.List(t.Context(), metav1.ListOptions{
			LabelSelector: "kube-bind.io/exported=true",
		})
		if err != nil {
			return false, fmt.Sprintf("Error listing schemas: %v", err)
		}
		if len(schemas.Items) == 0 {
			return false, "No exported APIResourceSchemas found yet"
		}
		// Verify at least one schema has the expected structure.
		for _, s := range schemas.Items {
			group, _, _ := unstructured.NestedString(s.Object, "spec", "group")
			if group == "wildwest.dev" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("Found %d schemas but none for wildwest.dev group", len(schemas.Items))
	}, wait.ForeverTestTimeout, time.Second)
}

func testKCP3TierResourceSync(t *testing.T, consumerCfg, backendCfg *rest.Config, scope kubebindv1alpha2.InformerScope, kind, resource string) {
	serviceGVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: resource}

	consumerClient := testKcpClient(t, consumerCfg, scope, serviceGVR, "default")
	backendClient := testKcpClient(t, backendCfg, scope, serviceGVR, "")

	t.Run("instance created downstream syncs upstream", func(t *testing.T) {
		t.Logf("Creating %s instance on consumer side", kind)

		resourceInstance := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "wildwest.dev/v1alpha1",
				"kind":       kind,
				"metadata": map[string]any{
					"name": "test-3tier-" + resource,
				},
				"spec": map[string]any{
					"intent": "draw",
				},
			},
		}

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := consumerClient.Create(t.Context(), resourceInstance, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("Error creating %s instance: %v", resource, err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		t.Logf("Waiting for %s instance to be synced to backend side", resource)
		var instances *unstructured.UnstructuredList
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var err error
			instances, err = backendClient.List(t.Context(), metav1.ListOptions{})
			return err == nil && len(instances.Items) >= 1, fmt.Sprintf("Error listing %s instances: %v", resource, err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		require.GreaterOrEqual(t, len(instances.Items), 1, "Expected at least one %s instance on backend side", resource)
	})

	t.Run("instance spec updated downstream syncs upstream", func(t *testing.T) {
		t.Logf("Updating %s spec on consumer side", resource)

		require.Eventually(t, func() bool {
			obj, err := consumerClient.Get(t.Context(), "test-3tier-"+resource, metav1.GetOptions{})
			if err != nil {
				return false
			}

			unstructured.SetNestedField(obj.Object, "holster", "spec", "intent") //nolint:errcheck
			_, err = consumerClient.Update(t.Context(), obj, metav1.UpdateOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, 5*time.Second, "waiting for %s spec to be updated on consumer side", resource)

		t.Logf("Waiting for %s spec update to sync to backend side", resource)
		require.Eventually(t, func() bool {
			instances, err := backendClient.List(t.Context(), metav1.ListOptions{})
			if err != nil || len(instances.Items) == 0 {
				return false
			}

			for _, item := range instances.Items {
				intent, found, err := unstructured.NestedString(item.Object, "spec", "intent")
				if err == nil && found && intent == "holster" {
					return true
				}
			}
			return false
		}, wait.ForeverTestTimeout, 5*time.Second, "waiting for %s spec update to sync to backend side", resource)
	})

	t.Run("instance deleted downstream is deleted upstream", func(t *testing.T) {
		t.Logf("Deleting %s instance on consumer side", resource)

		err := consumerClient.Delete(t.Context(), "test-3tier-"+resource, metav1.DeleteOptions{})
		require.NoError(t, err, "Failed to delete %s on consumer side", resource)

		t.Logf("Waiting for %s instance to be deleted on backend side", resource)
		require.Eventually(t, func() bool {
			instances, err := backendClient.List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return false
			}
			for _, item := range instances.Items {
				if item.GetName() == "test-3tier-"+resource {
					return false
				}
			}
			return true
		}, wait.ForeverTestTimeout, 5*time.Second, "waiting for %s instance to be deleted on backend side", resource)
	})
}
