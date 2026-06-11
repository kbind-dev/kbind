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

// Package framework provides an envtest-based harness for v2 slim-core e2e
// tests: a provider API server and a consumer API server, with the konnector
// engine reconcilers running in-process against the consumer.
//
// Unlike the v1 framework (kcp + backend + browser auth), the v2 core has no
// backend, so the harness only needs two API servers, a kubeconfig Secret, and
// the one-apply bundle.
package framework

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/kube-bind/kube-bind/v2/konnector/engine/binding"
	"github.com/kube-bind/kube-bind/v2/konnector/engine/connection"
	"github.com/kube-bind/kube-bind/v2/konnector/engine/provider"
	syncengine "github.com/kube-bind/kube-bind/v2/konnector/engine/sync"
	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// KubeBindNamespace is the konnector's designated namespace on the consumer.
const KubeBindNamespace = "kube-bind"

// Env is a running provider+consumer test environment with the engine wired up.
type Env struct {
	Scheme *apimachineryruntime.Scheme

	ProviderCfg    *rest.Config
	ConsumerCfg    *rest.Config
	ProviderClient client.Client
	ConsumerClient client.Client
	ProviderDyn    dynamic.Interface
	ConsumerDyn    dynamic.Interface
}

// Start brings up two envtest API servers, installs the core CRDs on the
// consumer, creates the kube-system namespaces (for cluster identity) and the
// kube-bind namespace, stores the provider kubeconfig as a Secret on the
// consumer, and starts the engine reconcilers in-process against the consumer.
var setLoggerOnce sync.Once

func Start(t *testing.T) *Env {
	t.Helper()
	setLoggerOnce.Do(func() { ctrl.SetLogger(zap.New(zap.UseDevMode(true))) })

	scheme := apimachineryruntime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))

	// Consumer API server, pre-loaded with the core CRDs.
	consumerEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{coreCRDDir(t)},
		ErrorIfCRDPathMissing: true,
		Scheme:                scheme,
	}
	consumerCfg, err := consumerEnv.Start()
	require.NoError(t, err, "starting consumer envtest")
	t.Cleanup(func() { _ = consumerEnv.Stop() })

	// Provider API server (plain).
	providerEnv := &envtest.Environment{Scheme: scheme}
	providerCfg, err := providerEnv.Start()
	require.NoError(t, err, "starting provider envtest")
	t.Cleanup(func() { _ = providerEnv.Stop() })

	consumerClient, err := client.New(consumerCfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	providerClient, err := client.New(providerCfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	consumerDyn, err := dynamic.NewForConfig(consumerCfg)
	require.NoError(t, err)
	providerDyn, err := dynamic.NewForConfig(providerCfg)
	require.NoError(t, err)

	ctx := context.Background()

	// kube-system in both clusters → stable cluster identity for ownership markers
	// (envtest may already seed it, so tolerate AlreadyExists).
	ensureNamespace(t, consumerClient, "kube-system")
	ensureNamespace(t, providerClient, "kube-system")
	ensureNamespace(t, consumerClient, KubeBindNamespace)

	// Store the provider kubeconfig as a Secret on the consumer (the credential
	// the Connection references).
	kubeconfig, err := kubeconfigFromRestConfig(providerCfg)
	require.NoError(t, err)
	require.NoError(t, consumerClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-provider-kubeconfig", Namespace: KubeBindNamespace},
		Data:       map[string][]byte{"kubeconfig": kubeconfig},
	}))

	startEngine(t, consumerCfg, scheme)

	return &Env{
		Scheme:         scheme,
		ProviderCfg:    providerCfg,
		ConsumerCfg:    consumerCfg,
		ProviderClient: providerClient,
		ConsumerClient: consumerClient,
		ProviderDyn:    providerDyn,
		ConsumerDyn:    consumerDyn,
	}
}

// startEngine wires the full engine the way main.go does: a local (consumer)
// manager, the mcr ConnectionProvider (each Connection -> engaged provider
// cluster), the multicluster manager, and the reconcilers — including the
// sync engine using the engaged cluster (Option B).
func startEngine(t *testing.T, consumerCfg *rest.Config, scheme *apimachineryruntime.Scheme) {
	t.Helper()
	localMgr, err := ctrl.NewManager(consumerCfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	connProvider, err := provider.New(localMgr, provider.Options{})
	require.NoError(t, err)

	mcMgr, err := mcmanager.New(consumerCfg, connProvider, mcmanager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	require.NoError(t, (&connection.Reconciler{}).SetupWithManager(localMgr))
	require.NoError(t, (&binding.ClusterReconciler{}).SetupWithManager(localMgr))
	require.NoError(t, (&binding.NamespacedReconciler{}).SetupWithManager(localMgr))
	require.NoError(t, syncengine.SetupWithManager(localMgr, connProvider))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := localMgr.Start(ctx); err != nil {
			t.Logf("local manager stopped: %v", err)
		}
	}()
	go func() {
		if err := connProvider.Run(ctx, mcMgr); err != nil {
			t.Logf("connection provider stopped: %v", err)
		}
	}()
	go func() {
		if err := mcMgr.Start(ctx); err != nil {
			t.Logf("mc manager stopped: %v", err)
		}
	}()
	require.True(t, localMgr.GetCache().WaitForCacheSync(ctx), "engine cache sync")
}

// InstallExportedWidgetCRD installs the demo Widget CRD on the provider, labeled
// as exported.
func (e *Env) InstallExportedWidgetCRD(t *testing.T) schema.GroupVersionResource {
	t.Helper()
	crd := widgetCRD()
	require.NoError(t, e.ProviderClient.Create(context.Background(), crd))
	gvr := schema.GroupVersionResource{Group: "example.org", Version: "v1", Resource: "widgets"}
	// Wait for the provider to serve the new API.
	require.Eventually(t, func() bool {
		_, err := e.ProviderDyn.Resource(gvr).Namespace("default").List(context.Background(), metav1.ListOptions{})
		return err == nil
	}, 30*time.Second, 200*time.Millisecond, "provider should serve the Widget API")
	return gvr
}

// WidgetGVK returns the demo Widget GVK.
func WidgetGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Widget"}
}

func widgetCRD() *apiextensionsv1.CustomResourceDefinition {
	preserve := true
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "widgets.example.org",
			Labels: map[string]string{corev1alpha1.LabelExported: "true"},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.org",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "widgets",
				Singular: "widget",
				Kind:     "Widget",
				ListKind: "WidgetList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    "v1",
				Served:  true,
				Storage: true,
				Subresources: &apiextensionsv1.CustomResourceSubresources{
					Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
				},
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec": {
								Type:                   "object",
								XPreserveUnknownFields: &preserve,
							},
							"status": {
								Type:                   "object",
								XPreserveUnknownFields: &preserve,
							},
						},
					},
				},
			}},
		},
	}
}

// kubeconfigFromRestConfig serializes an envtest rest.Config (client-cert auth)
// into a kubeconfig the konnector can load from a Secret.
func kubeconfigFromRestConfig(cfg *rest.Config) ([]byte, error) {
	const name = "provider"
	c := clientcmdapi.NewConfig()
	c.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	c.AuthInfos[name] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
	}
	c.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	c.CurrentContext = name
	return clientcmd.Write(*c)
}

func ensureNamespace(t *testing.T, c client.Client, name string) {
	t.Helper()
	err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err, "creating namespace %s", name)
	}
}

func coreCRDDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller")
	// .../v2/konnector/test/e2e/framework/framework.go -> .../v2/sdk/config/crd
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "sdk", "config", "crd")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	return abs
}

// WaitForConditionTrue polls until the named condition on the object is True.
func WaitForConditionTrue(t *testing.T, get func() ([]metav1.Condition, error), condType string) {
	t.Helper()
	require.Eventually(t, func() bool {
		conds, err := get()
		if err != nil {
			return false
		}
		for _, c := range conds {
			if c.Type == condType {
				return c.Status == metav1.ConditionTrue
			}
		}
		return false
	}, wait.ForeverTestTimeout, 200*time.Millisecond, "waiting for condition %s=True", condType)
}
