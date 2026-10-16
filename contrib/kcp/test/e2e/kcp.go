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

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	"github.com/kube-bind/kube-bind/contrib/kcp/bootstrap"
	"github.com/kube-bind/kube-bind/contrib/kcp/bootstrap/options"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	"github.com/kube-bind/kube-bind/test/e2e/framework"
)

// waitForClusterEngaged blocks until the kcp multicluster provider has engaged
// the workspace described by cfg. The provider creates a kube-bind.io Cluster
// named "default" in every workspace it engages, so we poll for that object.
func waitForClusterEngaged(t testing.TB, cfg *rest.Config) {
	t.Helper()

	gvr := schema.GroupVersionResource{
		Group:    kubebindv1alpha2.SchemeGroupVersion.Group,
		Version:  kubebindv1alpha2.SchemeGroupVersion.Version,
		Resource: "clusters",
	}

	client := framework.DynamicClient(t, cfg).Resource(gvr)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := client.Get(t.Context(), kubebindv1alpha2.DefaultClusterName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, "kube-bind.io Cluster \"default\" not created yet"
			}
			return false, fmt.Sprintf("error getting kube-bind.io Cluster: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

func wsConfig(t testing.TB, rest *rest.Config, workspace logicalcluster.Path) (*rest.Config, string) {
	cfg := rest
	cfg.Host += "/clusters/" + workspace.String()

	kubeconfig := framework.WriteKubeconfig(t,
		framework.RestToKubeconfig(cfg, "default"),
		workspace.Base()+".kubeconfig",
	)

	return cfg, kubeconfig
}

func generateApiBinding(t testing.TB, path logicalcluster.Path, name, exportName string, acceptPermissionClaims ...string) *kcpapisv1alpha2.APIBinding {
	t.Helper()

	apiBinding := &kcpapisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kcpapisv1alpha2.APIBindingSpec{
			Reference: kcpapisv1alpha2.BindingReference{
				Export: &kcpapisv1alpha2.ExportBindingReference{
					Path: path.String(),
					Name: exportName,
				},
			},
		},
	}

	for _, claim := range acceptPermissionClaims {
		split := strings.SplitN(claim, ".", 2)
		require.Len(t, split, 2, "invalid permission claim: %s, must be in the form <resource>.<group>", claim)

		resource, group := split[0], split[1]
		require.NotEmpty(t, group, "invalid permission claim: %s, group cannot be empty", claim)
		require.NotEmpty(t, resource, "invalid permission claim: %s, resource cannot be empty", claim)

		if group == "core" {
			group = ""
		}

		apiBinding.Spec.PermissionClaims = append(
			apiBinding.Spec.PermissionClaims,
			kcpapisv1alpha2.AcceptablePermissionClaim{
				State: kcpapisv1alpha2.ClaimAccepted,
				ScopedPermissionClaim: kcpapisv1alpha2.ScopedPermissionClaim{
					Selector: kcpapisv1alpha2.PermissionClaimSelector{
						MatchAll: true,
					},
					PermissionClaim: kcpapisv1alpha2.PermissionClaim{
						GroupResource: kcpapisv1alpha2.GroupResource{
							Group:    group,
							Resource: resource,
						},
						Verbs: []string{"*"},
					},
				},
			},
		)
	}

	return apiBinding
}

func createApiBinding(t testing.TB, client *kcpclientset.ClusterClientset, path logicalcluster.Path, apiBinding *kcpapisv1alpha2.APIBinding) *kcpapisv1alpha2.APIBinding {
	t.Helper()

	_, err := client.Cluster(path).
		ApisV1alpha2().
		APIBindings().
		Create(t.Context(), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating APIBinding")

	var inCluster *kcpapisv1alpha2.APIBinding
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		inCluster, err = client.
			Cluster(path).
			ApisV1alpha2().
			APIBindings().
			Get(t.Context(), apiBinding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Error getting APIBinding: %v", err)
		}
		return inCluster.Status.Phase == kcpapisv1alpha2.APIBindingPhaseBound, fmt.Sprintf("APIBinding not ready: %#v", inCluster.Status)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	return inCluster
}

// bootstrapKCP bootstraps kcp for a test. It creates a per-test root workspace
// (root:<test>) with a kbind workspace beneath it (root:<test>:kbind) and installs
// the kube-bind APIs there. It returns a rest.Config pointing at the per-test root
// workspace (so consumer/provider workspaces can be created as siblings of kbind)
// and the path of the kbind workspace.
func bootstrapKCP(t testing.TB, rest *rest.Config) (*rest.Config, logicalcluster.Path) {
	t.Helper()
	t.Log("Bootstrapping kcp")

	wsName := strings.ToLower(t.Name())
	kbindLeafName := "kbind"
	rootWorkspace := fmt.Sprintf("root:%s", wsName)
	kbindWorkspace := fmt.Sprintf("%s:%s", rootWorkspace, kbindLeafName)

	// The provided rest.Config should already point to a running kcp server.
	// It should already have /clusters/root in the path if read from kubeconfig.
	// Just ensure we have the right base URL for bootstrap.
	cfg := *rest
	adminApiCfg := framework.RestToKubeconfig(&cfg, "default")

	// create the two workspaces we want our tests to run in
	testRootCfg, _ := framework.NewWorkspace(t, rest, func(ws *kcptenancyv1alpha1.Workspace) {
		ws.Name = wsName
		ws.GenerateName = ""
	})

	framework.NewWorkspace(t, testRootCfg, func(ws *kcptenancyv1alpha1.Workspace) {
		ws.Name = kbindLeafName
		ws.GenerateName = ""
	})

	adminKubeconfig := framework.WriteKubeconfigToPath(t, adminApiCfg, filepath.Join(framework.WorkDir(t), "admin.kubeconfig"))

	fs := pflag.NewFlagSet("kcp-bootstrap", pflag.ContinueOnError)
	options := options.NewOptions()
	options.AddFlags(fs)

	options.KCPKubeConfig = adminKubeconfig
	options.RootWorkspace = rootWorkspace
	options.KBindWorkspace = kbindWorkspace

	completed, err := options.Complete()
	require.NoError(t, err)

	config, err := bootstrap.NewConfig(completed)
	require.NoError(t, err)

	bootstrapper, err := bootstrap.NewServer(t.Context(), config)
	require.NoError(t, err)

	err = bootstrapper.Start(t.Context())
	require.NoError(t, err)

	return testRootCfg, logicalcluster.NewPath(kbindWorkspace)
}
