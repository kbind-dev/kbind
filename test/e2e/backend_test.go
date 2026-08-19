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

package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kbind/kbind/backend/auth"
	"github.com/kbind/kbind/backend/gateway"
	"github.com/kbind/kbind/backend/issuer"
	"github.com/kbind/kbind/backend/reaper"
	"github.com/kbind/kbind/cli"
	"github.com/kbind/kbind/pkg/kubeapply"
	"github.com/kbind/kbind/pkg/servicecrds"
	catalogv1alpha1 "github.com/kbind/kbind/sdk/apis/catalog/v1alpha1"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
	"github.com/kbind/kbind/test/e2e/framework"
)

// staticAuth is a test Authenticator: fixed bearer tokens map to identities.
type staticAuth struct{ tokens map[string]*auth.Identity }

func (s *staticAuth) Name() string                    { return "test" }
func (s *staticAuth) RegisterRoutes(_ *http.ServeMux) {}
func (s *staticAuth) Authenticate(r *http.Request) (*auth.Identity, error) {
	if id, ok := s.tokens[auth.TokenFromRequest(r)]; ok {
		return id, nil
	}
	return nil, auth.ErrUnauthenticated
}

// TestBackendFullLoop exercises the whole extended layer against the real
// core: catalog → gateway bind → issuer-provisioned credentials → one-apply
// bundle → konnector sync through the issued RBAC-fenced ServiceAccount →
// heartbeat in the boundary namespace → revocation, plus the reaper on a
// grant whose bundle was never applied.
func TestBackendFullLoop(t *testing.T) {
	env := framework.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backendScheme := apimachineryruntime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(backendScheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(backendScheme))
	utilruntime.Must(corev1alpha1.AddToScheme(backendScheme))
	utilruntime.Must(catalogv1alpha1.AddToScheme(backendScheme))
	utilruntime.Must(iamv1alpha1.AddToScheme(backendScheme))

	// The provider serves the service-layer CRDs — via the backend's own
	// startup self-install path (what --install-crds runs).
	require.NoError(t, servicecrds.Install(ctx, env.ProviderCfg),
		"installing catalog/iam CRDs on the provider")

	providerClient, err := client.New(env.ProviderCfg, client.Options{Scheme: backendScheme})
	require.NoError(t, err)

	env.InstallExportedWidgetCRD(t)

	startProviderControllers(t, ctx, env, backendScheme)
	startFakeTokenController(t, ctx, env, providerClient)

	// Curate the catalog: one Export over the widget API, in one Collection.
	require.NoError(t, providerClient.Create(ctx, &catalogv1alpha1.Export{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets"},
		Spec: catalogv1alpha1.ExportSpec{
			Title:       "Widgets",
			Description: "Managed widgets with automated pointiness.",
			APIs:        []corev1alpha1.APIRef{{Name: "widgets.example.org"}},
			Defaults: catalogv1alpha1.BindingDefaults{
				RelatedResources: []corev1alpha1.RelatedResource{{
					Resource:  "secrets",
					Direction: corev1alpha1.FromProvider,
					Selector: &corev1alpha1.RelatedResourceSelector{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"widgets.example.org/related": "true"},
						},
					},
				}},
			},
		},
	}))
	require.NoError(t, providerClient.Create(ctx, &catalogv1alpha1.Collection{
		ObjectMeta: metav1.ObjectMeta{Name: "hardware"},
		Spec: catalogv1alpha1.CollectionSpec{
			Title:   "Hardware",
			Exports: []catalogv1alpha1.ExportRef{{Name: "widgets"}},
		},
	}))
	framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
		var e catalogv1alpha1.Export
		if err := providerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, &e); err != nil {
			return nil, err
		}
		return e.Status.Conditions, nil
	}, catalogv1alpha1.ConditionReady)

	// The gateway, fronting exactly this provider.
	alice := &auth.Identity{Subject: "https://sso.example.com#alice", DisplayName: "alice@example.com"}
	bob := &auth.Identity{Subject: "https://sso.example.com#bob", DisplayName: "bob@example.com"}
	gw := gateway.New(gateway.Config{
		ProviderName: "e2e-provider",
		BindTimeout:  time.Minute,
	}, providerClient, &issuer.BundleBuilder{
		Client:          providerClient,
		ExternalAddress: env.ProviderCfg.Host,
		ExternalCA:      env.ProviderCfg.CAData,
	}, []auth.Authenticator{
		&staticAuth{tokens: map[string]*auth.Identity{
			"token-alice": alice,
			"token-bob":   bob,
		}},
		mustKubernetesAuth(t, env),
	}, nil)
	server := httptest.NewServer(gw.Handler())
	t.Cleanup(server.Close)

	aliceClient := cli.NewClient(server.URL, "token-alice")

	t.Run("provider metadata and catalog", func(t *testing.T) {
		provider, err := aliceClient.Provider(ctx)
		require.NoError(t, err)
		require.Equal(t, "e2e-provider", provider.Name)
		require.Equal(t, []string{"test", "kubernetes"}, provider.AuthMethods)
		require.False(t, provider.ApplyEnabled)

		_, err = cli.NewClient(server.URL, "").Catalog(ctx)
		require.ErrorIs(t, err, cli.ErrLoginRequired, "catalog requires auth")

		catalog, err := aliceClient.Catalog(ctx)
		require.NoError(t, err)
		require.Len(t, catalog.Exports, 1)
		require.Equal(t, "widgets", catalog.Exports[0].Name)
		require.Len(t, catalog.Collections, 1)

		// Related resources (a core concept) surface on the offering.
		require.Len(t, catalog.Exports[0].RelatedResources, 1)
		rr := catalog.Exports[0].RelatedResources[0]
		require.Equal(t, "secrets", rr.Resource)
		require.Equal(t, "FromProvider", rr.Direction)
		require.Equal(t, "widgets.example.org/related=true", rr.Selector)
	})

	t.Run("konnector install manifest is served for connecting clusters", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/konnector", nil)
		require.NoError(t, err)
		res, err := http.DefaultClient.Do(req) // no auth: curl|kubectl is a complete client
		require.NoError(t, err)
		defer func() { _ = res.Body.Close() }()
		require.Equal(t, http.StatusOK, res.StatusCode)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		manifest := string(body)
		require.Contains(t, manifest, "kind: CustomResourceDefinition")
		require.Contains(t, manifest, "kind: Deployment")
		require.Contains(t, manifest, "name: konnector")
	})

	t.Run("kubernetes authenticator accepts a provider-cluster token", func(t *testing.T) {
		// An in-platform caller that already holds a provider identity: a
		// ServiceAccount token minted on the provider, verified by the
		// gateway via TokenReview — no SSO round trip.
		clientset, err := kubernetes.NewForConfig(env.ProviderCfg)
		require.NoError(t, err)
		_, err = clientset.CoreV1().ServiceAccounts("default").Create(ctx,
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "portal"}}, metav1.CreateOptions{})
		require.NoError(t, err)
		tr, err := clientset.CoreV1().ServiceAccounts("default").CreateToken(ctx, "portal",
			&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: ptr.To(int64(3600)),
			}}, metav1.CreateOptions{})
		require.NoError(t, err)

		catalog, err := cli.NewClient(server.URL, tr.Status.Token).Catalog(ctx)
		require.NoError(t, err)
		require.Len(t, catalog.Exports, 1)
	})

	var bundle []byte
	grantName := issuer.GrantName("widgets", alice.Subject)
	boundaryNS := issuer.BoundaryNamespace(alice.Subject)

	t.Run("bind provisions credentials and delivers a single-use bundle", func(t *testing.T) {
		res, err := aliceClient.Bind(ctx, "widgets")
		require.NoError(t, err)
		require.Equal(t, grantName, res.Grant)

		bundle, err = aliceClient.Pickup(ctx, res.PickupURL)
		require.NoError(t, err)
		require.Contains(t, string(bundle), "kind: Connection")
		require.Contains(t, string(bundle), "kind: ClusterBinding")

		_, err = aliceClient.Pickup(ctx, res.PickupURL)
		require.Error(t, err, "pickup is single-use")

		// The issuer's artifacts exist: SA, token, RBAC, in the boundary.
		var sa corev1.ServiceAccount
		require.NoError(t, providerClient.Get(ctx, client.ObjectKey{Namespace: boundaryNS, Name: grantName}, &sa))
		var role rbacv1.Role
		require.NoError(t, providerClient.Get(ctx, client.ObjectKey{Namespace: boundaryNS, Name: grantName}, &role))
	})

	t.Run("one apply of the bundle syncs through issued credentials", func(t *testing.T) {
		applier, err := kubeapply.New(env.ConsumerCfg, "e2e")
		require.NoError(t, err)
		objs, err := kubeapply.DecodeYAML(bundle)
		require.NoError(t, err)
		_, err = applier.Apply(ctx, objs)
		require.NoError(t, err)

		framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
			var conn corev1alpha1.Connection
			if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: grantName}, &conn); err != nil {
				return nil, err
			}
			return conn.Status.Conditions, nil
		}, corev1alpha1.ConditionReady)
		framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
			var cb corev1alpha1.ClusterBinding
			if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: grantName}, &cb); err != nil {
				return nil, err
			}
			return cb.Status.Conditions, nil
		}, corev1alpha1.ConditionReady)

		// Spec up: a consumer widget materializes on the provider — written by
		// the issued ServiceAccount, fenced by the issued RBAC.
		widget := &unstructured.Unstructured{}
		widget.SetGroupVersionKind(framework.WidgetGVK())
		widget.SetNamespace("default")
		widget.SetName("w-backend")
		require.NoError(t, unstructured.SetNestedField(widget.Object, "pointy", "spec", "shape"))
		require.NoError(t, env.ConsumerClient.Create(ctx, widget))

		require.Eventually(t, func() bool {
			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(framework.WidgetGVK())
			return env.ProviderClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "w-backend"}, got) == nil
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "widget should sync to the provider")

		// The heartbeat Lease lands in the boundary namespace (the issued
		// kubeconfig pins its context namespace there) — the reaper's signal.
		require.Eventually(t, func() bool {
			var leases coordinationv1.LeaseList
			if err := providerClient.List(ctx, &leases, client.InNamespace(boundaryNS),
				client.MatchingLabels{corev1alpha1.LabelManaged: "true"}); err != nil {
				return false
			}
			return len(leases.Items) > 0
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "heartbeat Lease in the boundary namespace")
	})

	t.Run("clusters view shows the consumer cluster and what it syncs", func(t *testing.T) {
		// Bob binds but never applies his bundle: he must show up as
		// pending, not as a cluster.
		_, err := cli.NewClient(server.URL, "token-bob").Bind(ctx, "widgets")
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			clusters, err := aliceClient.Clusters(ctx)
			if err != nil || len(clusters.Clusters) != 1 {
				return false
			}
			cluster := clusters.Clusters[0]
			if !cluster.Live || len(cluster.Bindings) != 1 {
				return false
			}
			binding := cluster.Bindings[0]
			if binding.Grant != grantName || binding.Export != "widgets" ||
				binding.Identity != alice.DisplayName || !binding.Live {
				return false
			}
			// The synced widget from the previous subtest is attributed to
			// this cluster.
			if len(binding.APIs) != 1 || binding.APIs[0].Name != "widgets.example.org" ||
				binding.APIs[0].SyncedCount < 1 {
				return false
			}
			// Bob: bound, never connected.
			for _, p := range clusters.Pending {
				if p.Export == "widgets" && p.Identity == bob.DisplayName {
					return true
				}
			}
			return false
		}, wait.ForeverTestTimeout, 200*time.Millisecond,
			"clusters view should show alice's live cluster with a synced widget and bob as pending")
	})

	t.Run("catalog instances view lists synced objects per export", func(t *testing.T) {
		// A provider original matching the export's FromProvider related rule
		// shows up as a related row ("delivered with this service").
		require.NoError(t, providerClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "widgets-connection",
				Namespace: "default",
				Labels:    map[string]string{"widgets.example.org/related": "true"},
			},
			StringData: map[string]string{"endpoint": "https://widgets.internal"},
		}))

		require.Eventually(t, func() bool {
			res, err := aliceClient.Instances(ctx, "widgets")
			if err != nil || len(res.Instances) != 2 {
				return false
			}
			widget, related := res.Instances[0], res.Instances[1]
			if widget.API != "widgets.example.org" || widget.Related ||
				widget.Namespace != "default" || widget.Name != "w-backend" ||
				widget.ClusterUID == "" || widget.Identity != alice.DisplayName {
				return false
			}
			return related.API == "secrets" && related.Related &&
				related.Direction == "FromProvider" &&
				related.Namespace == "default" && related.Name == "widgets-connection" &&
				related.ClusterUID == ""
		}, wait.ForeverTestTimeout, 200*time.Millisecond,
			"the synced widget and the related secret original should both be listed")

		_, err := aliceClient.Instances(ctx, "no-such-export")
		require.Error(t, err, "unknown export is an error")
	})

	t.Run("reaper reaps a grant whose bundle was never applied", func(t *testing.T) {
		bobClient := cli.NewClient(server.URL, "token-bob")
		res, err := bobClient.Bind(ctx, "widgets")
		require.NoError(t, err)
		bobGrant := res.Grant
		require.NotEqual(t, grantName, bobGrant)

		kubeIssuer := &issuer.KubeIssuer{Client: providerClient}
		go (&reaper.Reaper{
			Client:   providerClient,
			Issuer:   kubeIssuer,
			TTL:      3 * time.Second,
			Revoke:   true,
			Interval: 500 * time.Millisecond,
		}).Start(ctx) //nolint:errcheck

		require.Eventually(t, func() bool {
			var g iamv1alpha1.Grant
			return apierrors.IsNotFound(providerClient.Get(ctx, client.ObjectKey{Name: bobGrant}, &g))
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "stale grant should be revoked")

		// Bob's credentials are gone with it.
		var sa corev1.ServiceAccount
		require.Eventually(t, func() bool {
			return apierrors.IsNotFound(providerClient.Get(ctx,
				client.ObjectKey{Namespace: issuer.BoundaryNamespace(bob.Subject), Name: bobGrant}, &sa))
		}, wait.ForeverTestTimeout, 200*time.Millisecond)

		// Alice keeps heartbeating; her grant survives the same reaper.
		var g iamv1alpha1.Grant
		require.NoError(t, providerClient.Get(ctx, client.ObjectKey{Name: grantName}, &g))
	})

	t.Run("deleting the grant revokes the credentials", func(t *testing.T) {
		var g iamv1alpha1.Grant
		require.NoError(t, providerClient.Get(ctx, client.ObjectKey{Name: grantName}, &g))
		require.NoError(t, providerClient.Delete(ctx, &g))

		require.Eventually(t, func() bool {
			var sa corev1.ServiceAccount
			return apierrors.IsNotFound(providerClient.Get(ctx, client.ObjectKey{Namespace: boundaryNS, Name: grantName}, &sa))
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "revocation removes the ServiceAccount")

		// The boundary namespace (and the synced objects) survive plain
		// revocation — destructive cleanup is the reaper's explicit opt-in.
		var ns corev1.Namespace
		require.NoError(t, providerClient.Get(ctx, client.ObjectKey{Name: boundaryNS}, &ns))
	})
}

func mustKubernetesAuth(t *testing.T, env *framework.Env) *auth.Kubernetes {
	t.Helper()
	k, err := auth.NewKubernetes(env.ProviderCfg)
	require.NoError(t, err)
	return k
}

// startProviderControllers runs the issuer's Grant + Export reconcilers
// against the provider, the way cmd/backend does.
func startProviderControllers(t *testing.T, ctx context.Context, env *framework.Env, scheme *apimachineryruntime.Scheme) {
	t.Helper()
	mgr, err := ctrl.NewManager(env.ProviderCfg, ctrl.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, (&issuer.GrantReconciler{Issuer: &issuer.KubeIssuer{Client: mgr.GetClient()}}).SetupWithManager(mgr))
	require.NoError(t, (&issuer.ExportReconciler{}).SetupWithManager(mgr))
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Logf("provider manager stopped: %v", err)
		}
	}()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx), "provider controller cache sync")
}

// startFakeTokenController plays kube-controller-manager's token controller,
// which envtest does not run: it populates service-account-token Secrets with
// a real TokenRequest token (accepted by the envtest API server) and the CA.
func startFakeTokenController(t *testing.T, ctx context.Context, env *framework.Env, providerClient client.Client) {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(env.ProviderCfg)
	require.NoError(t, err)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			var secrets corev1.SecretList
			if err := providerClient.List(ctx, &secrets); err != nil {
				continue
			}
			for i := range secrets.Items {
				secret := &secrets.Items[i]
				if secret.Type != corev1.SecretTypeServiceAccountToken || len(secret.Data[corev1.ServiceAccountTokenKey]) > 0 {
					continue
				}
				saName := secret.Annotations[corev1.ServiceAccountNameKey]
				if saName == "" {
					continue
				}
				tr, err := clientset.CoreV1().ServiceAccounts(secret.Namespace).CreateToken(ctx, saName,
					&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
						ExpirationSeconds: ptr.To(int64(24 * 3600)),
					}}, metav1.CreateOptions{})
				if err != nil {
					continue
				}
				if secret.Data == nil {
					secret.Data = map[string][]byte{}
				}
				secret.Data[corev1.ServiceAccountTokenKey] = []byte(tr.Status.Token)
				secret.Data[corev1.ServiceAccountRootCAKey] = env.ProviderCfg.CAData
				secret.Data["namespace"] = []byte(secret.Namespace)
				_ = providerClient.Update(ctx, secret)
			}
		}
	}()
}
