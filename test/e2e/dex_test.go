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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const dexImage = "ghcr.io/dexidp/dex:v2.41.1"

// TestBackendWithDex boots the REAL backend binary (cmd/backend) against a
// real OIDC issuer — Dex in docker — and an envtest provider, then drives the
// full authorization-code login through Dex's password form: proof that the
// kube-style OIDC flags, discovery, PKCE exchange and session issuance work
// against an actual IdP, not just the embedded mock. Skips without docker.
func TestBackendWithDex(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("docker not available; skipping Dex e2e")
	}

	// A provider API server for the backend to run against (the backend
	// self-installs the catalog/iam CRDs at startup).
	providerEnv := &envtest.Environment{}
	providerCfg, err := providerEnv.Start()
	require.NoError(t, err, "starting provider envtest")
	t.Cleanup(func() { _ = providerEnv.Stop() })

	kubeconfigPath := filepath.Join(t.TempDir(), "provider.kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfigPath, kubeconfigBytes(t, providerCfg), 0o600))

	gatewayPort := freePort(t)
	dexPort := freePort(t)
	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d", gatewayPort)
	dexIssuer := fmt.Sprintf("http://127.0.0.1:%d/dex", dexPort)

	startDex(t, ctx, dexPort, gatewayURL+"/api/auth/oidc/callback", dexIssuer)
	startBackendBinary(t, ctx, kubeconfigPath, gatewayPort, dexIssuer)

	// The gateway is up and advertises OIDC.
	var provider struct {
		AuthMethods []string `json:"authMethods"`
	}
	require.Eventually(t, func() bool {
		res, err := http.Get(gatewayURL + "/api/provider") //nolint:noctx // poll helper
		if err != nil {
			return false
		}
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&provider) == nil
	}, 60*time.Second, 500*time.Millisecond, "backend gateway should come up")
	require.Equal(t, []string{"oidc"}, provider.AuthMethods)

	// The browser dance, through Dex's real login form.
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	browser := &http.Client{Jar: jar}

	loginReq, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/api/auth/oidc/login?redirect=/", nil)
	require.NoError(t, err)
	res, err := browser.Do(loginReq)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Request.URL.String(), "/dex/auth", "should land on Dex's login form")

	// Submit the password form to the page Dex served (its URL carries the
	// request id); skipApprovalScreen then bounces straight back to the
	// gateway callback, which issues the session and redirects home.
	form := url.Values{"login": {"admin@example.com"}, "password": {"password"}}
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, res.Request.URL.String(),
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res2, err := browser.Do(postReq)
	require.NoError(t, err)
	defer func() { _ = res2.Body.Close() }()
	require.Equal(t, http.StatusOK, res2.StatusCode, "login should end back on the gateway")
	require.Equal(t, fmt.Sprintf("127.0.0.1:%d", gatewayPort), res2.Request.URL.Host,
		"final redirect should land on the gateway, got %s", res2.Request.URL)

	// The session works: /api/me knows who we are, via the email claim.
	meReq, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/api/me", nil)
	require.NoError(t, err)
	res3, err := browser.Do(meReq)
	require.NoError(t, err)
	defer func() { _ = res3.Body.Close() }()
	require.Equal(t, http.StatusOK, res3.StatusCode)
	var me struct {
		Subject     string `json:"subject"`
		DisplayName string `json:"displayName"`
	}
	require.NoError(t, json.NewDecoder(res3.Body).Decode(&me))
	require.Equal(t, dexIssuer+"#admin@example.com", me.Subject)
	require.Equal(t, "admin@example.com", me.DisplayName)
}

// startDex runs Dex in docker with memory storage, one static client (the
// gateway) and one static password user.
func startDex(t *testing.T, ctx context.Context, port int, redirectURI, issuer string) {
	t.Helper()

	// bcrypt of "password" (the canonical hash from Dex's own examples).
	config := fmt.Sprintf(`issuer: %s
storage:
  type: memory
web:
  http: 0.0.0.0:5556
staticClients:
  - id: kbind
    secret: kbind-secret
    name: kbind
    redirectURIs:
      - %s
oauth2:
  skipApprovalScreen: true
enablePasswordDB: true
staticPasswords:
  - email: admin@example.com
    hash: "$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W"
    username: admin
    userID: 08a8684b-db88-4b73-90a9-3cd1661f5466
`, issuer, redirectURI)
	configPath := filepath.Join(t.TempDir(), "dex.yaml")
	// 0644: the file is bind-mounted into the Dex container, whose user must
	// read it (0600 would be root-only inside the container).
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o644)) //nolint:gosec // see above

	name := fmt.Sprintf("kbind-e2e-dex-%d", port)
	run := exec.CommandContext(ctx, "docker", "run", "--rm", "-d",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:5556", port),
		"-v", configPath+":/etc/dex/config.yaml:ro",
		dexImage, "dex", "serve", "/etc/dex/config.yaml")
	out, err := run.CombinedOutput()
	require.NoError(t, err, "starting dex: %s", out)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() })

	require.Eventually(t, func() bool {
		res, err := http.Get(issuer + "/.well-known/openid-configuration") //nolint:noctx // poll helper
		if err != nil {
			return false
		}
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode == http.StatusOK
	}, 60*time.Second, 500*time.Millisecond, "dex discovery should come up")
}

// startBackendBinary builds and runs the real cmd/backend with kube-style
// OIDC flags pointing at Dex.
func startBackendBinary(t *testing.T, ctx context.Context, kubeconfig string, gatewayPort int, dexIssuer string) {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "backend")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/backend")
	build.Dir = repoRoot(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building backend: %s", out)

	backend := exec.CommandContext(ctx, bin,
		fmt.Sprintf("--listen-address=127.0.0.1:%d", gatewayPort),
		fmt.Sprintf("--external-url=http://127.0.0.1:%d", gatewayPort),
		"--oidc-issuer-url="+dexIssuer,
		"--oidc-client-id=kbind",
		"--oidc-client-secret=kbind-secret",
		"--oidc-username-claim=email",
		"--metrics-bind-address=0",
		"--health-probe-bind-address=127.0.0.1:0",
	)
	backend.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	var logs bytes.Buffer
	backend.Stdout = &logs
	backend.Stderr = &logs
	require.NoError(t, backend.Start())
	t.Cleanup(func() {
		_ = backend.Process.Kill()
		_, _ = backend.Process.Wait()
		if t.Failed() {
			t.Logf("backend logs:\n%s", logs.String())
		}
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	require.NoError(t, err)
	return root
}

// kubeconfigBytes serializes an envtest rest.Config into a kubeconfig file
// the backend binary can load via KUBECONFIG.
func kubeconfigBytes(t *testing.T, cfg *rest.Config) []byte {
	t.Helper()
	c := clientcmdapi.NewConfig()
	c.Clusters["provider"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	c.AuthInfos["provider"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
	}
	c.Contexts["provider"] = &clientcmdapi.Context{Cluster: "provider", AuthInfo: "provider"}
	c.CurrentContext = "provider"
	data, err := clientcmd.Write(*c)
	require.NoError(t, err)
	return data
}
