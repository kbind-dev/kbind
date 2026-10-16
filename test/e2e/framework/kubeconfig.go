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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func ClientConfig(t *testing.T) *rest.Config {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = os.Getenv("KUBECONFIG")
	clientcmdConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil)
	config, err := clientcmdConfig.ClientConfig()
	require.NoError(t, err)

	return config
}

func RestToKubeconfig(config *rest.Config, namespace string) clientcmdapi.Config {
	return clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"default": {
				Server:                   config.Host,
				CertificateAuthorityData: config.CAData,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {
				Cluster:   "default",
				Namespace: namespace,
				AuthInfo:  "default",
			},
		},
		CurrentContext: "default",
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default": {
				Token: config.BearerToken,
			},
		},
	}
}

func WriteKubeconfig(t testing.TB, config clientcmdapi.Config, name string) string {
	t.Helper()

	return WriteKubeconfigToPath(t, config, filepath.Join(t.TempDir(), name))
}

func WriteKubeconfigToPath(t testing.TB, config clientcmdapi.Config, path string) string {
	t.Helper()
	err := clientcmd.WriteToFile(config, path)
	require.NoError(t, err, "error writing kubeconfig to %q", path)
	return path
}
