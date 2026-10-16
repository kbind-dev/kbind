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

package framework

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/martinlindhe/base36"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
)

type (
	ClusterWorkspaceOption func(ws *tenancyv1alpha1.Workspace)
)

var (
	kcpScheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(tenancyv1alpha1.AddToScheme(kcpScheme))
	utilruntime.Must(kubebindv1alpha2.AddToScheme(kcpScheme))
}

func WithName(s string, formatArgs ...any) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Name = fmt.Sprintf(s, formatArgs...)
		ws.GenerateName = ""
	}
}

func WithStaticName(s string) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Name = s
		ws.GenerateName = ""
	}
}

func RandomString(length int) string {
	token := make([]byte, length)
	rand.Read(token)
	base36hash := strings.ToLower(base36.EncodeBytes(token))
	return base36hash[:length+1]
}

func WithGenerateName(s string, formatArgs ...any) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		s = fmt.Sprintf(s, formatArgs...)
		// Workspace.ObjectMeta.GenerateName is broken in kcp: https://github.com/kcp-dev/kcp/pull/2193
		//
		// ws.GenerateName = fmt.Sprintf(s, formatArgs...)
		// if !strings.HasSuffix(ws.GenerateName, "-") {
		// ws.GenerateName += "-"
		// }
		if !strings.HasSuffix(s, "-") {
			s += "-"
		}

		token := make([]byte, 4)
		rand.Read(token)
		base36hash := strings.ToLower(base36.EncodeBytes(token))
		ws.Name = s + base36hash[:5]
		ws.GenerateName = ""
	}
}

func NewWorkspace(t testing.TB, config *rest.Config, options ...ClusterWorkspaceOption) (*rest.Config, string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	httpClient, err := rest.HTTPClientFor(config)
	require.NoError(t, err)
	mapper, err := apiutil.NewDynamicRESTMapper(config, httpClient)
	require.NoError(t, err)
	tenancyClient, err := client.New(config, client.Options{Scheme: kcpScheme, Mapper: mapper})
	require.NoError(t, err)

	ws := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
		},
	}

	// workaround broken GenerateName for workspaces: https://github.com/kcp-dev/kcp/pull/2193
	ws.Name = ws.GenerateName
	ws.GenerateName = ""
	token := make([]byte, 4)
	rand.Read(token)
	base36hash := strings.ToLower(base36.EncodeBytes(token))
	ws.Name += base36hash[:5]

	for _, opt := range options {
		opt(ws)
	}
	// retry as on startup kcp might be busy
	require.Eventually(t, func() bool {
		err = tenancyClient.Create(ctx, ws)
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace %s", ws.Name)

	ret := rest.CopyConfig(config)
	ret.Host += ":" + ws.Name

	wsKubeconfigPath := filepath.Join(WorkDir(t), ws.Name+".kubeconfig")
	err = clientcmd.WriteToFile(RestToKubeconfig(ret, ""), wsKubeconfigPath)
	require.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
		defer cancelFn()

		err := tenancyClient.Get(ctx, client.ObjectKey{Name: ws.Name}, ws)
		if errors.IsNotFound(err) {
			return
		}
		require.NoError(t, err)
		tenancyClient.Delete(ctx, ws) //nolint:errcheck
		os.Remove(wsKubeconfigPath)
	})

	require.Eventually(t, func() bool {
		background, cancel := context.WithDeadline(context.Background(), metav1.Now().Add(10*time.Second))
		defer cancel()
		err := tenancyClient.Get(background, client.ObjectKey{Name: ws.Name}, ws)
		require.NoError(t, err)
		return ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for workspace %s to become ready", ws.Name)
	t.Logf("Created %s workspace %s", ws.Spec.Type, ws.Name)

	return ret, wsKubeconfigPath
}
