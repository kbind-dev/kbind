/*
Copyright 2022 The Kube Bind Authors.

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

package bind

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"

	kuberesources "github.com/kube-bind/kube-bind/backend/kubernetes/resources"
	bindapiservice "github.com/kube-bind/kube-bind/cli/pkg/kubectl/bind-apiservice/plugin"
	examples "github.com/kube-bind/kube-bind/deploy/examples"
	"github.com/kube-bind/kube-bind/pkg/konnector/types"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	"github.com/kube-bind/kube-bind/test/e2e/framework"
)

// TestClusterScoped tests scenarios where consumer-side resources are cluster-scoped (Sheriffs).
// Uses: template-sheriffs.yaml (scope=Cluster) & cr-sheriff.yaml
//
// This test validates three isolation strategies for cluster-scoped resources:
//   - IsolationPrefixed: Sheriff name is prefixed with consumer ID, secrets isolated
//   - IsolationNone: Same names and namespaces on both sides (⚠️ no isolation!)
//   - IsolationNamespaced: CRD toggled to NamespaceScoped on provider, secrets isolated
//
// For detailed architecture diagrams and explanations, see:
// docs/content/developers/architecture.md#cluster-scoped-resources
func TestClusterScoped(t *testing.T) {
	// name & test type defined by letters - cc - cluster-cluster so its easier to identify failures in the logs.
	testHappyCase(t, "cc-prefixed", apiextensionsv1.ClusterScoped, apiextensionsv1.ClusterScoped, kubebindv1alpha2.ClusterScope, kubebindv1alpha2.IsolationPrefixed)
	testHappyCase(t, "cc-none", apiextensionsv1.ClusterScoped, apiextensionsv1.ClusterScoped, kubebindv1alpha2.ClusterScope, kubebindv1alpha2.IsolationNone)
	testHappyCase(t, "cc-namespaced", apiextensionsv1.ClusterScoped, apiextensionsv1.NamespaceScoped, kubebindv1alpha2.ClusterScope, kubebindv1alpha2.IsolationNamespaced)
}

// TestNamespacedScoped tests scenarios where consumer-side resources are namespaced (Cowboys).
// Uses: template-cowboys.yaml (scope=Namespaced) & cr-cowboy.yaml
//
// This test validates two informerScope configurations for namespaced resources:
//   - NamespacedScope (nn): Konnector watches only its own namespace on provider
//   - ClusterScope (nc): Konnector watches cluster-wide on provider
//
// Note: Namespaced resources ALWAYS use ServiceNamespaced isolation strategy,
// regardless of the isolation parameter. Namespace mapping is always:
// wild-west → kube-bind-<consumer-id>-wild-west
//
// For detailed architecture diagrams and explanations, see:
// docs/content/developers/architecture.md#namespaced-resources
func TestNamespacedScoped(t *testing.T) {
	testHappyCase(t, "nn", apiextensionsv1.NamespaceScoped, apiextensionsv1.NamespaceScoped, kubebindv1alpha2.NamespacedScope, "")
	testHappyCase(t, "nc", apiextensionsv1.NamespaceScoped, apiextensionsv1.NamespaceScoped, kubebindv1alpha2.ClusterScope, "")
}

// testHappyCase is the main test function that validates end-to-end kube-bind functionality.
// It tests the complete lifecycle of binding, syncing, and managing resources between
// consumer and provider clusters.
//
// TEST EXECUTION FLOW (with 2 consumers for isolation verification):
//
//	SETUP PHASE
//	===========
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 1. Create provider workspace (kcp)                                       │
//	│    - Install kube-bind CRDs                                              │
//	│    - Start backend server (HTTP API for binding)                         │
//	│    - Bootstrap example CRDs (Cowboys/Sheriffs) via examples.Bootstrap()  │
//	│    - Apply templates (template-cowboys.yaml / template-sheriffs.yaml)    │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 2. Create consumer workspaces (consumer1, consumer2)                     │
//	│    - Start konnector on each consumer (watches APIServiceBinding)        │
//	└──────────────────────────────────────────────────────────────────────────┘
//
//	BINDING PHASE (executed for each consumer)
//	=============
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 3. Login to provider                                                     │
//	│    - Simulate browser auth flow                                          │
//	│    - Save credentials to kube-bind-config.yaml                           │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 4. List templates & collections                                          │
//	│    - Verify backend returns 2 templates (cowboys, sheriffs)              │
//	│    - Verify 1 collection exists                                          │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 5. Bind API (consumer.Bind())                                            │
//	│    - Send BindableResourcesRequest with:                                 │
//	│      • templateRef: "cowboys" or "sheriffs"                              │
//	│      • clusterIdentity: <unique-uuid>                                    │
//	│    - Backend creates on provider:                                        │
//	│      • Contract namespace: kube-bind-<consumer-id-hash>                  │
//	│      • APIServiceExportRequest                                           │
//	│      • APIServiceNamespace: wild-west → kube-bind-<id>-wild-west        │
//	│      • RBAC resources for secret access                                  │
//	│    - Backend returns BindingResourceResponse with CRDs, RBAC manifests   │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 6. Apply binding on consumer (BindFromResponse())                        │
//	│    - Create APIServiceBinding (watched by konnector)                     │
//	│    - Apply CRDs (Cowboys.wildwest.dev or Sheriffs.wildwest.dev)          │
//	│    - Wait for CRD to be established                                      │
//	└──────────────────────────────────────────────────────────────────────────┘
//
//	SYNCHRONIZATION & VERIFICATION PHASE (per consumer)
//	===================================
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 7. Create instance on consumer (from cr-cowboy.yaml / cr-sheriff.yaml)   │
//	│    - Namespace: wild-west                                                │
//	│    - Resource: billy-the-kid (Cowboy) or wyatt-earp (Sheriff)           │
//	│    - Secrets: colt-45-permit, cowboy-gang-affiliation, etc.              │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 8. Verify sync to provider                                               │
//	│    ✓ Instance created on provider (with ClusterNamespaceAnnotation)      │
//	│    ✓ providerContractNamespace extracted from annotation                 │
//	│    ✓ providerObjectNamespace captured (may be empty for cluster-scoped)  │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 9. Verify namespace pre-seeding & RBAC                                   │
//	│    ✓ APIServiceNamespace created with .status.namespace populated        │
//	│    ✓ Actual provider namespace exists (kube-bind-<id>-wild-west)         │
//	│    ✓ RBAC created (ClusterRole/Role + Bindings) for secret access        │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 10. Test bi-directional sync                                             │
//	│     ✓ Update spec on consumer → synced to provider                       │
//	│     ✓ Update status on provider → synced to consumer                     │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 11. Verify secret sync (from permissionClaims in template)               │
//	│     ✓ Referenced secret (spec.secretRefs) synced to provider namespace   │
//	│     ✓ Label-selected secret (app=cowboy/sheriff) synced                  │
//	│     Location: kube-bind-<consumer-id>-wild-west/                         │
//	└──────────────────────────────────────────────────────────────────────────┘
//	                                ▼
//	┌──────────────────────────────────────────────────────────────────────────┐
//	│ 12. Test deletion                                                         │
//	│     ✓ Delete on consumer → deleted on provider                           │
//	└──────────────────────────────────────────────────────────────────────────┘
//
// MULTI-CONSUMER ISOLATION VERIFICATION:
// ======================================
// Running with 2 consumers validates that:
//   - Each consumer gets isolated contract namespace (kube-bind-<consumer1-hash>, kube-bind-<consumer2-hash>)
//   - Each consumer gets isolated provider namespace (kube-bind-<consumer1-hash>-wild-west, etc.)
//   - Secrets are fully isolated between consumers
//   - For cluster-scoped resources with IsolationNone, last-write-wins (both write to same Sheriff name)
//   - For cluster-scoped resources with IsolationPrefixed, resources are name-prefixed
//   - For cluster-scoped resources with IsolationNamespaced, provider CRD is toggled to NamespaceScoped
func testHappyCase(
	t *testing.T,
	name string,
	consumerResourceScope apiextensionsv1.ResourceScope,
	providerResourceScope apiextensionsv1.ResourceScope,
	informerScope kubebindv1alpha2.InformerScope,
	isolationStrategy kubebindv1alpha2.Isolation,
) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // Commented out to prevent cleanup of kcp assets

	suffix := framework.RandomString(4)

	t.Logf("Creating provider workspace")
	providerConfig, providerKubeconfig := framework.NewWorkspace(t, framework.ClientConfig(t), framework.WithName("%s-provider-%s", name, suffix))

	t.Logf("Installing kubebind CRDs")
	framework.InstallKubeBindCRDs(t, providerConfig)

	t.Logf("Starting backend with random port")
	addr, _ := framework.StartBackend(t,
		"--kubeconfig="+providerKubeconfig,
		"--listen-address=:0",
		"--consumer-scope="+string(informerScope),
		"--isolation="+string(isolationStrategy),
	)

	t.Logf("Creating CRD on provider side")
	examples.Bootstrap(t, framework.DiscoveryClient(t, providerConfig), framework.DynamicClient(t, providerConfig), nil)

	// For namespaced-isolated cluster-scoped objects, the CRD needs to be namespaced on the provider side,
	// but cluster-scoped on the consumer side. To make this setup possible without introducing another CRD,
	// we will simply "hack" the CRD here on the providerside to make it work.
	if providerResourceScope == apiextensionsv1.NamespaceScoped && consumerResourceScope == apiextensionsv1.ClusterScoped {
		t.Logf("Changing sheriff CRD scope to namespaced on the provider side")
		toggleCRDScope(t, ctx, providerConfig, "sheriffs.wildwest.dev", apiextensionsv1.NamespaceScoped)
	}

	t.Logf("Creating consumer workspace and starting konnector")
	consumer1Config, consumer1Kubeconfig := framework.NewWorkspace(t, framework.ClientConfig(t),
		framework.WithGenerateName("%s-consumer-%s-", name, suffix))
	consumer2Config, consumer2Kubeconfig := framework.NewWorkspace(t, framework.ClientConfig(t),
		framework.WithGenerateName("%s-consumer-%s-", name, suffix))
	framework.StartKonnector(t, consumer1Config, "--kubeconfig="+consumer1Kubeconfig, "--server-address=:0")
	framework.StartKonnector(t, consumer2Config, "--kubeconfig="+consumer2Kubeconfig, "--server-address=:0")

	serviceGVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}
	templateRef := "cowboys"

	if consumerResourceScope == apiextensionsv1.ClusterScoped {
		serviceGVR = schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"}
		templateRef = "sheriffs"
	}

	consumer1Client := framework.DynamicClient(t, consumer1Config).Resource(serviceGVR)
	consumer2Client := framework.DynamicClient(t, consumer2Config).Resource(serviceGVR)
	providerClient := framework.DynamicClient(t, providerConfig).Resource(serviceGVR)

	providerCoreClient := framework.KubeClient(t, providerConfig).CoreV1()
	providerBindClient := framework.BindClient(t, providerConfig)

	kubeBindConfig := path.Join(framework.WorkDir(t), "kube-bind-config.yaml")

	// Secrets from the actual examples:
	// For cowboys: colt-45-permit (referenced), cowboy-gang-affiliation (label selector)
	// For sheriffs: sheriff-badge-credentials (referenced), sheriff-jurisdiction-config (label selector)
	var referencedSecretName, labelSelectedSecretName string
	var filename string
	if consumerResourceScope == apiextensionsv1.NamespaceScoped {
		referencedSecretName = "colt-45-permit"
		labelSelectedSecretName = "cowboy-gang-affiliation"
		filename = "cr-cowboy.yaml"
	} else {
		referencedSecretName = "sheriff-badge-credentials"
		labelSelectedSecretName = "sheriff-jurisdiction-config"
		filename = "cr-sheriff.yaml"
	}

	// Initial setup steps
	for _, tc := range []struct {
		name string
		step func(t *testing.T)
	}{
		{
			name: "Login to provider",
			step: func(t *testing.T) {
				iostreams, _, _, _ := genericclioptions.NewTestIOStreams()
				authURLDryRunCh := make(chan string, 1)
				go framework.SimulateBrowser(t, authURLDryRunCh)
				framework.Login(t, iostreams, authURLDryRunCh, kubeBindConfig, fmt.Sprintf("http://%s/api/exports", addr.String()), "")
			},
		},
		{
			name: "List templates",
			step: func(t *testing.T) {
				c := framework.GetKubeBindRestClient(t, kubeBindConfig)
				result, err := c.GetTemplates(ctx)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, result.Items, 2)
			},
		},
		{
			name: "List collections",
			step: func(t *testing.T) {
				c := framework.GetKubeBindRestClient(t, kubeBindConfig)
				result, err := c.GetCollections(ctx)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, result.Items, 1)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.step(t)
		})
	}

	// Consumer-specific test loops
	for _, consumer := range []struct {
		// name is unique name usined in naming instances
		name string
		// clusterIdentity is unique UUID representing the consumer cluster identity.
		clusterIdentity uuid.UUID
		// consumerConfig is the REST config for the consumer cluster.
		consumerConfig *rest.Config
		// consumerKubeconfig is the kubeconfig path for the consumer cluster.
		consumerKubeconfig string
		// consumerClient is the dynamic client for the main resource on the consumer side.
		consumerClient dynamic.NamespaceableResourceInterface

		// Bellow are runtime values populated during the test run and stored in the same struct
		// for easier access.

		// providerContractNamespace is the namespace representing the binding contract on the backend side.
		// YOu can get it from every synced object annotation.
		providerContractNamespace string
		// providerObjectNamespace is the namespace on the provider side where the synced object resides. Might be empty for cluster-scoped.
		providerObjectNamespace string
		// consumerObjectNamespace is the namespace on the consumer side where the main object resides.
		consumerObjectNamespace string
		// bindResponse is the response from the bind API call for this consumer. This contains dedicated provider
		// resources for this consumer.
		bindResponse *kubebindv1alpha2.BindingResourceResponse
	}{
		{
			name:               "consumer1",
			clusterIdentity:    uuid.Must(uuid.NewUUID()),
			consumerConfig:     consumer1Config,
			consumerKubeconfig: consumer1Kubeconfig,
			consumerClient:     consumer1Client,

			providerContractNamespace: "unknown",
			providerObjectNamespace:   "unknown",
			consumerObjectNamespace:   "wild-west", // This is part of binding contract and should be honored
			bindResponse:              nil,
		},
		{
			name:               "consumer2",
			clusterIdentity:    uuid.Must(uuid.NewUUID()),
			consumerConfig:     consumer2Config,
			consumerKubeconfig: consumer2Kubeconfig,
			consumerClient:     consumer2Client,

			providerContractNamespace: "unknown",
			providerObjectNamespace:   "unknown",
			consumerObjectNamespace:   "wild-west",
			bindResponse:              nil,
		},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				step func(t *testing.T)
			}{
				{
					name: "Get bind APIServiceExportRequest from server",
					step: func(t *testing.T) {
						c := framework.GetKubeBindRestClient(t, kubeBindConfig)
						var err error

						consumer.bindResponse, err = c.Bind(ctx, &kubebindv1alpha2.BindableResourcesRequest{
							ObjectMeta: metav1.ObjectMeta{
								Name: "test-binding",
							},
							Spec: kubebindv1alpha2.BindableResourcesRequestSpec{
								TemplateRef: kubebindv1alpha2.APIServiceExportTemplateRef{
									Name: templateRef,
								},
								ClusterIdentity: kubebindv1alpha2.ClusterIdentity{
									Identity: consumer.clusterIdentity.String(),
								},
							},
						})
						require.NoError(t, err)
						require.NotNil(t, *consumer.bindResponse)
					},
				},
				{
					name: "Bind the payload on consumer side",
					step: func(t *testing.T) {
						iostreams, _, _, _ := genericclioptions.NewTestIOStreams()
						binderOpts := &bindapiservice.BinderOptions{
							IOStreams:     iostreams,
							SkipKonnector: true,
						}

						binder := bindapiservice.NewBinder(consumer.consumerConfig, binderOpts)
						result, err := binder.BindFromResponse(ctx, consumer.bindResponse)
						require.NoError(t, err)
						require.Len(t, result, 1)

						t.Logf("Waiting for %s CRD to be created on consumer side", serviceGVR.Resource)
						crdClient := framework.ApiextensionsClient(t, consumer.consumerConfig).ApiextensionsV1().CustomResourceDefinitions()
						require.Eventually(t, func() bool {
							if serviceGVR.Group == "" {
								serviceGVR.Group = "core"
							}
							_, err := crdClient.Get(ctx, serviceGVR.Resource+"."+serviceGVR.Group, metav1.GetOptions{})
							return err == nil
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for %s CRD to be created on consumer side", serviceGVR.Resource)
					},
				},
				{
					name: "instances are synced",
					step: func(t *testing.T) {
						instanceName := fmt.Sprintf("test-%s", consumer.name)
						t.Logf("Seeding example objects on consumer side for %s with instance name %s", serviceGVR.Resource, instanceName)

						err := seedSpecificCRExample(t, framework.DynamicClient(t, consumer.consumerConfig), filename, instanceName)
						require.NoError(t, err)

						t.Logf("Trying to create %s on consumer side", serviceGVR.Resource)

						t.Logf("Waiting for the %s instance to be created on provider side", serviceGVR.Resource)
						var instances *unstructured.UnstructuredList
						require.Eventually(t, func() bool {
							var err error
							instances, err = providerClient.List(ctx, metav1.ListOptions{})
							return err == nil && len(instances.Items) >= 1
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for the %s instance to be created on provider side", serviceGVR.Resource)

						// Find our specific instance
						var ourInstance *unstructured.Unstructured
						for _, item := range instances.Items {
							if strings.Contains(item.GetName(), instanceName) {
								ourInstance = &item
								break
							}
						}
						require.NotNil(t, ourInstance, "should find instance for consumer %s", consumer.name)

						// providerNs is the namespaced where the synced object lives; this might be empty
						// for cluster-scoped objects on the provider side.
						consumer.providerObjectNamespace = ourInstance.GetNamespace()
						// Cluster namespace represent binding contract namespace, and is stored on every object.
						consumer.providerContractNamespace = ourInstance.GetAnnotations()[types.ClusterNamespaceAnnotationKey]
						require.NotEmpty(t, consumer.providerContractNamespace, "cluster namespace annotation must always exist")
						require.NotEqual(t, consumer.providerContractNamespace, "unknown")

						// Verify consumer source metadata annotations are set on the provider-side object.
						annotations := ourInstance.GetAnnotations()
						_, hasConsumerNS := annotations[types.ConsumerNamespaceAnnotationKey]
						require.True(t, hasConsumerNS, "consumer-namespace annotation must exist on provider object")
						consumerUID := annotations[types.ConsumerUIDAnnotationKey]
						require.NotEmpty(t, consumerUID, "consumer-uid annotation must exist on provider object")
					},
				},
				{
					name: "verify BoundSchemas have owner references to APIServiceExport",
					step: func(t *testing.T) {
						t.Logf("Verifying BoundSchemas have owner references to APIServiceExport")
						export, err := providerBindClient.KubeBindV1alpha2().APIServiceExports(consumer.providerContractNamespace).Get(ctx, "test-binding", metav1.GetOptions{})
						if errors.IsNotFound(err) && consumer.name != "consumer1" {
							t.Skip("APIServiceExport already deleted by another consumer")
						}
						require.NoError(t, err, "APIServiceExport should exist")

						boundSchemas, err := providerBindClient.KubeBindV1alpha2().BoundSchemas(consumer.providerContractNamespace).List(ctx, metav1.ListOptions{})
						require.NoError(t, err, "should be able to list BoundSchemas")
						require.NotEmpty(t, boundSchemas.Items, "should have at least one BoundSchema")

						// Verify each BoundSchema has an owner reference to the APIServiceExport
						for _, boundSchema := range boundSchemas.Items {
							t.Logf("Checking owner reference for BoundSchema %s", boundSchema.Name)

							var hasOwnerRef bool
							for _, ownerRef := range boundSchema.OwnerReferences {
								if ownerRef.Kind == "APIServiceExport" &&
									ownerRef.Name == export.Name &&
									ownerRef.UID == export.UID &&
									ownerRef.Controller != nil &&
									*ownerRef.Controller {
									hasOwnerRef = true
									break
								}
							}

							require.True(t, hasOwnerRef,
								"BoundSchema %s should have controller owner reference to APIServiceExport %s",
								boundSchema.Name, export.Name)
						}

						t.Logf("All BoundSchemas have proper owner references")
					},
				},
				// Request included namespace, so we check it first
				{
					name: "verify provider side namespace pre-seeding and RBAC management",
					step: func(t *testing.T) {
						t.Logf("Verifying APIServiceNamespace was created from pre-seeded namespace spec")
						var foundPreSeededNamespace bool
						var actualProviderNamespace string
						require.Eventually(t, func() bool {
							// If we are operating namespaced resources - namespace will be set, if cluster - we need to use
							// extracted one from the cluster-scoped object.
							namespaces, err := providerBindClient.KubeBindV1alpha2().APIServiceNamespaces(consumer.providerContractNamespace).List(ctx, metav1.ListOptions{})
							if err != nil {
								return false
							}

							for _, ns := range namespaces.Items {
								if ns.Name == "wild-west" && ns.Status.Namespace != "" {
									actualProviderNamespace = ns.Status.Namespace
									foundPreSeededNamespace = true
									return true
								}
							}
							return false
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for pre-seeded APIServiceNamespace to be created on provider side")
						require.True(t, foundPreSeededNamespace, "Pre-seeded namespace 'wild-west' should be created via APIServiceExportRequest.Spec.Namespaces")

						providerCoreClient := framework.KubeClient(t, providerConfig).CoreV1()
						_, err := providerCoreClient.Namespaces().Get(ctx, actualProviderNamespace, metav1.GetOptions{})
						require.NoError(t, err, "Actual provider side namespace object should exist")

						expectedVerbs := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
						slices.Sort(expectedVerbs)

						switch informerScope {
						case kubebindv1alpha2.ClusterScope:
							t.Logf("Verifying RBAC resources were created for secret management in cluster scope")
							rbacClient := framework.KubeClient(t, providerConfig).RbacV1()

							require.Eventually(t, func() bool {
								clusterRoles, err := rbacClient.ClusterRoles().List(ctx, metav1.ListOptions{})
								require.NoError(t, err)
								for _, clusterRole := range clusterRoles.Items {
									if !strings.HasPrefix(clusterRole.Name, "kube-binder-claims-") {
										continue
									}
									for _, rule := range clusterRole.Rules {
										if !slices.Contains(rule.Resources, "secrets") || !slices.Contains(rule.APIGroups, "") {
											continue
										}
										slices.Sort(rule.Verbs)
										if slices.Equal(expectedVerbs, rule.Verbs) {
											return true
										}
									}
								}
								return false
							}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for ClusterRole for claimed secrets resource to be ready on provider side")

							t.Logf("Verifying ClusterRole for secrets claim has been aggregated into kube-binder-exports ClusterRole")

							require.Eventually(t, func() bool {
								aggregatingClusterRole, err := rbacClient.ClusterRoles().Get(ctx, "kube-binder-exports", metav1.GetOptions{})
								require.NoError(t, err) // kube-binder-exports must already exist: it is created before kube-binder-claims-* ClusterRole.
								for _, rule := range aggregatingClusterRole.Rules {
									if !slices.Contains(rule.Resources, "secrets") || !slices.Contains(rule.APIGroups, "") {
										continue
									}
									slices.Sort(rule.Verbs)
									if slices.Equal(expectedVerbs, rule.Verbs) {
										return true
									}
								}
								return false
							}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for ClusterRoleBinding to be ready on provider side")

							t.Logf("Verifying ClusterRoleBinding for kube-binder-exports ClusterRole was created")

							require.Eventually(t, func() bool {
								aggregatingCRB, err := rbacClient.ClusterRoleBindings().Get(ctx, "kube-binder-exports", metav1.GetOptions{})
								require.NoError(t, err)
								for _, subject := range aggregatingCRB.Subjects {
									if subject.Kind == "ServiceAccount" && subject.Name == kuberesources.ServiceAccountName {
										return true
									}
								}
								return false
							}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for ClusterRoleBinding kube-binder-exports to be ready on provider side")
						case kubebindv1alpha2.NamespacedScope:
							t.Logf("Verifying RBAC resources were created for secret management in namespace scope")
							rbacClient := framework.KubeClient(t, providerConfig).RbacV1()

							require.Eventually(t, func() bool {
								roles, err := rbacClient.Roles(consumer.providerObjectNamespace).List(ctx, metav1.ListOptions{})
								require.NoError(t, err)
								for _, cr := range roles.Items {
									if !strings.HasPrefix(cr.Name, "kube-binder-claims-") {
										continue
									}
									for _, rule := range cr.Rules {
										if !slices.Contains(rule.Resources, "secrets") || !slices.Contains(rule.APIGroups, "") {
											continue
										}
										slices.Sort(rule.Verbs)
										if slices.Equal(expectedVerbs, rule.Verbs) {
											return true
										}
									}
								}
								return false
							}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for RoleBinding for claimed secrets resource to be ready on provider side")

							t.Logf("Verifying RoleBinding was created for pre-seeded namespace secret access")

							require.Eventually(t, func() bool {
								roleBindings, err := rbacClient.RoleBindings(consumer.providerObjectNamespace).List(ctx, metav1.ListOptions{})
								require.NoError(t, err)
								for _, rb := range roleBindings.Items {
									if !strings.HasPrefix(rb.Name, "kube-binder-claims-") {
										continue
									}
									for _, subject := range rb.Subjects {
										if subject.Kind == "ServiceAccount" && subject.Name == kuberesources.ServiceAccountName {
											return true
										}
									}
								}
								return false
							}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for RoleBinding for claimed secrets resource to be ready on provider side")
						}

						t.Logf("Provider side namespace pre-seeding and secret management RBAC verified successfully")
					},
				},
				{
					name: "instance spec updated downstream is updated upstream",
					step: func(t *testing.T) {
						instanceName := fmt.Sprintf("test-%s", consumer.name)
						err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
							var obj *unstructured.Unstructured
							var err error
							if consumerResourceScope == apiextensionsv1.NamespaceScoped {
								obj, err = consumer.consumerClient.Namespace(consumer.consumerObjectNamespace).Get(ctx, instanceName, metav1.GetOptions{})
							} else {
								obj, err = consumer.consumerClient.Get(ctx, instanceName, metav1.GetOptions{})
							}
							require.NoError(t, err)
							if consumerResourceScope == apiextensionsv1.NamespaceScoped {
								unstructured.SetNestedField(obj.Object, fmt.Sprintf("Updated cowboy intent from %s", consumer.name), "spec", "intent") //nolint:errcheck
								_, err = consumer.consumerClient.Namespace(consumer.consumerObjectNamespace).Update(ctx, obj, metav1.UpdateOptions{})
							} else {
								unstructured.SetNestedField(obj.Object, fmt.Sprintf("Updated sheriff intent from %s", consumer.name), "spec", "intent") //nolint:errcheck
								_, err = consumer.consumerClient.Update(ctx, obj, metav1.UpdateOptions{})
							}
							return err
						})
						require.NoError(t, err)

						// Find our instance on provider side
						require.Eventually(t, func() bool {
							instances, err := providerClient.List(ctx, metav1.ListOptions{})
							if err != nil {
								return false
							}
							for _, item := range instances.Items {
								if !strings.Contains(item.GetName(), instanceName) {
									continue
								}
								var obj *unstructured.Unstructured
								if providerResourceScope == apiextensionsv1.NamespaceScoped {
									obj, err = providerClient.Namespace(item.GetNamespace()).Get(ctx, instanceName, metav1.GetOptions{})
								} else {
									obj, err = providerClient.Get(ctx, item.GetName(), metav1.GetOptions{})
								}
								if err != nil {
									return false
								}
								value, _, err := unstructured.NestedString(obj.Object, "spec", "intent")
								require.NoError(t, err)
								if consumerResourceScope == apiextensionsv1.NamespaceScoped {
									return value == fmt.Sprintf("Updated cowboy intent from %s", consumer.name)
								} else {
									return value == fmt.Sprintf("Updated sheriff intent from %s", consumer.name)
								}
							}
							return false
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for the %s instance to be updated upstream for %s", serviceGVR.Resource, consumer.name)
					},
				},
				{
					name: "instance status updated upstream is updated downstream",
					step: func(t *testing.T) {
						instanceName := fmt.Sprintf("test-%s", consumer.name)
						// Find our instance on provider side first
						var providerInstanceName string
						var providerInstanceNs string
						instances, err := providerClient.List(ctx, metav1.ListOptions{})
						require.NoError(t, err)
						for _, item := range instances.Items {
							if strings.Contains(item.GetName(), instanceName) {
								providerInstanceName = item.GetName()
								providerInstanceNs = item.GetNamespace()
								break
							}
						}
						require.NotEmpty(t, providerInstanceName, "should find provider instance for %s", consumer.name)

						err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
							var obj *unstructured.Unstructured
							var err error
							if providerResourceScope == apiextensionsv1.NamespaceScoped {
								obj, err = providerClient.Namespace(providerInstanceNs).Get(ctx, instanceName, metav1.GetOptions{})
							} else {
								obj, err = providerClient.Get(ctx, providerInstanceName, metav1.GetOptions{})
							}
							require.NoError(t, err)
							unstructured.SetNestedField(obj.Object, fmt.Sprintf("Ready to ride from %s", consumer.name), "status", "result") //nolint:errcheck
							if providerResourceScope == apiextensionsv1.NamespaceScoped {
								_, err = providerClient.Namespace(providerInstanceNs).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
							} else {
								_, err = providerClient.UpdateStatus(ctx, obj, metav1.UpdateOptions{})
							}
							return err
						})
						require.NoError(t, err)

						var consumerObj *unstructured.Unstructured
						require.Eventually(t, func() bool {
							var err error
							if consumerResourceScope == apiextensionsv1.NamespaceScoped {
								consumerObj, err = consumer.consumerClient.Namespace(consumer.consumerObjectNamespace).Get(ctx, instanceName, metav1.GetOptions{})
							} else {
								consumerObj, err = consumer.consumerClient.Get(ctx, instanceName, metav1.GetOptions{})
							}
							require.NoError(t, err)
							value, _, err := unstructured.NestedString(consumerObj.Object, "status", "result")
							require.NoError(t, err)
							return value == fmt.Sprintf("Ready to ride from %s", consumer.name)
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for the %s instance to be updated downstream for %s", serviceGVR.Resource, consumer.name)

						// Verify provider source metadata annotations are set on the consumer-side object.
						consumerAnnotations := consumerObj.GetAnnotations()
						_, hasProviderNS := consumerAnnotations[types.ProviderNamespaceAnnotationKey]
						require.True(t, hasProviderNS, "provider-namespace annotation must exist on consumer object")
						providerUID := consumerAnnotations[types.ProviderUIDAnnotationKey]
						require.NotEmpty(t, providerUID, "provider-uid annotation must exist on consumer object")
					},
				},

				{
					name: "verify secrets from examples are synced to provider",
					step: func(t *testing.T) {
						// Determine which permission namespace to use for this consumer
						var consumerPermClaimNs string
						if informerScope == kubebindv1alpha2.ClusterScope &&
							consumerResourceScope == apiextensionsv1.ClusterScoped {
							// Find the specific permission claim namespace for this consumer
							namespaces, err := providerBindClient.KubeBindV1alpha2().APIServiceNamespaces(consumer.providerContractNamespace).List(ctx, metav1.ListOptions{})
							require.NoError(t, err)
							for _, namespace := range namespaces.Items {
								if strings.Contains(namespace.Name, consumer.consumerObjectNamespace) && namespace.Status.Namespace != "" {
									consumerPermClaimNs = namespace.Status.Namespace
									break
								}
							}
							require.NotEmpty(t, consumerPermClaimNs, "Should find permission claim namespace for %s", consumer.name)
						} else {
							consumerPermClaimNs = consumer.providerObjectNamespace
						}

						t.Logf("Waiting for referenced secret to be synced to provider side for %s in namespace %s", consumer.name, consumerPermClaimNs)
						require.Eventually(t, func() bool {
							_, err := providerCoreClient.Secrets(consumerPermClaimNs).Get(ctx, referencedSecretName, metav1.GetOptions{})
							return err == nil
						}, time.Minute*2, time.Millisecond*100, "waiting for referenced secret to be synced to provider side for %s", consumer.name)

						t.Logf("Waiting for label-selected secret to be synced to provider side for %s", consumer.name)
						require.Eventually(t, func() bool {
							_, err := providerCoreClient.Secrets(consumerPermClaimNs).Get(ctx, labelSelectedSecretName, metav1.GetOptions{})
							return err == nil
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for label-selected secret to be synced to provider side for %s", consumer.name)

						t.Logf("Secrets from examples are properly synced to provider for %s", consumer.name)
					},
				},
				{
					name: "instances deleted downstream are deleted upstream",
					step: func(t *testing.T) {
						instanceName := fmt.Sprintf("test-%s", consumer.name)
						// Find our instance on provider side first
						var providerInstanceName string
						instances, err := providerClient.List(ctx, metav1.ListOptions{})
						require.NoError(t, err)
						for _, item := range instances.Items {
							if strings.Contains(item.GetName(), instanceName) {
								providerInstanceName = item.GetName()
								break
							}
						}
						require.NotEmpty(t, providerInstanceName, "should find provider instance for %s", consumer.name)

						if consumerResourceScope == apiextensionsv1.NamespaceScoped {
							err = consumer.consumerClient.Namespace(consumer.consumerObjectNamespace).Delete(ctx, instanceName, metav1.DeleteOptions{})
						} else {
							err = consumer.consumerClient.Delete(ctx, instanceName, metav1.DeleteOptions{})
						}
						require.NoError(t, err)

						require.Eventually(t, func() bool {
							var err error
							if providerResourceScope == apiextensionsv1.NamespaceScoped {
								_, err = providerClient.Namespace(consumer.providerObjectNamespace).Get(ctx, instanceName, metav1.GetOptions{})
							} else {
								_, err = providerClient.Get(ctx, providerInstanceName, metav1.GetOptions{})
							}
							return errors.IsNotFound(err)
						}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for the %s instance to be deleted on provider side for %s", serviceGVR.Resource, consumer.name)
					},
				},
			} {
				t.Run(tc.name, tc.step)
			}
		})
	}
}

func seedSpecificCRExample(t *testing.T, dynamicClient dynamic.Interface, filename, instanceName string) error {
	t.Helper()

	ctx := t.Context()

	// Read and apply all objects from the specified CR example file
	bytes, err := examples.CRExamples.ReadFile(filename)
	if err != nil {
		return err
	}

	// Replace the main resource name with the test name
	yamlContent := string(bytes)
	if strings.Contains(filename, "cowboy") {
		yamlContent = strings.ReplaceAll(yamlContent, "name: billy-the-kid", "name: "+instanceName)
	} else if strings.Contains(filename, "sheriff") {
		yamlContent = strings.ReplaceAll(yamlContent, "name: wyatt-earp", "name: "+instanceName)
	}

	return applyMultiDocYAML(ctx, t, dynamicClient, yamlContent)
}

func applyMultiDocYAML(ctx context.Context, t *testing.T, dynamicClient dynamic.Interface, yamlContent string) error {
	t.Helper()

	// Split YAML documents
	documents := strings.Split(yamlContent, "---")

	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" || strings.HasPrefix(doc, "#") {
			continue
		}

		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(doc), obj); err != nil {
			continue // Skip invalid documents
		}

		if obj.GetKind() == "" {
			continue // Skip empty documents
		}

		gvk := obj.GroupVersionKind()
		gvr := schema.GroupVersionResource{
			Group:   gvk.Group,
			Version: gvk.Version,
			// Resources is handled below.
		}

		// Handle special cases for pluralization
		switch gvk.Kind {
		case "Namespace":
			gvr.Resource = "namespaces"
		case "Secret":
			gvr.Resource = "secrets"
		case "Cowboy":
			gvr.Resource = "cowboys"
		case "Sheriff":
			gvr.Resource = "sheriffs"
		}

		client := dynamicClient.Resource(gvr)

		// Apply based on namespace scope
		if obj.GetNamespace() != "" {
			_, err := client.Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
			if err != nil && !errors.IsAlreadyExists(err) {
				return err
			}
		} else {
			_, err := client.Create(ctx, obj, metav1.CreateOptions{})
			if err != nil && !errors.IsAlreadyExists(err) {
				return err
			}
		}
	}

	return nil
}

func toggleCRDScope(t *testing.T, ctx context.Context, config *rest.Config, name string, scope apiextensionsv1.ResourceScope) {
	clientset, err := apiextensionsclient.NewForConfig(config)
	require.NoError(t, err)

	crdClient := clientset.ApiextensionsV1().CustomResourceDefinitions()

	// copy existing CRD
	crd, err := crdClient.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)

	// delete it
	require.NoError(t, crdClient.Delete(ctx, name, metav1.DeleteOptions{}))

	require.Eventually(t, func() bool {
		_, err := crdClient.Get(ctx, name, metav1.GetOptions{})
		return errors.IsNotFound(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for the CRD to be deleted")

	// re-create it
	crd.Spec.Scope = scope
	crd.ObjectMeta.ResourceVersion = ""
	crd.ObjectMeta.UID = ""
	crd.ObjectMeta.Generation = 0
	_, err = crdClient.Create(ctx, crd, metav1.CreateOptions{})
	require.NoError(t, err)
}
