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

package framework

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	dexapi "github.com/dexidp/dex/api/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	grpcinsecure "google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var dexOnce sync.Once

var dexKill = func(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	// Do nothing. Without a suite setup and teardown we cannot
	// determine when to kill dex and Pdeathsig is only supported on
	// Linux.
	// On macOS (and Windows) the process will continue to run.
	// Which per se isn't a problem for development.
	cmd.SysProcAttr = &syscall.SysProcAttr{}
}

func StartDex(t testing.TB) {
	t.Helper()

	dexOnce.Do(func() {
		dexConfig := os.Getenv("DEX_CONFIG")
		if dexConfig == "" {
			dexConfig = filepath.Clean(filepath.Join(RepoRoot(), "hack", "dex-config-dev.yaml"))
		}

		t.Logf("Starting dex with config %q", dexConfig)

		dexBinary := os.Getenv("DEX_BINARY")
		if dexBinary == "" {
			dexBinary = "dex"
		}

		//nolint:gosec // test helper intentionally allows overriding dex binary/config via environment for local e2e runs.
		// Use context.Background() instead of t.Context() because dexOnce.Do
		// ties the process to the first test that calls StartDex. If we used
		// t.Context(), Dex would be killed when that test finishes, but
		// dexOnce prevents restarting it for subsequent tests.
		// The Dex process will be cleaned up when the test binary exits.
		dexCmd := exec.CommandContext(
			context.Background(),
			dexBinary,
			"serve",
			dexConfig,
		)

		// Set os-dependent killing
		dexKill(t, dexCmd)

		require.NoError(t, dexCmd.Start())
	})

	t.Log("Wait for Dex to be ready")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:5556/dex/.well-known/openid-configuration", nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	t.Log("Dex is ready")
}

func CreateDexClient(t testing.TB, addr net.Addr) (string, string) {
	t.Helper()

	_, port, err := net.SplitHostPort(addr.String())
	require.NoError(t, err)
	conn, err := grpc.NewClient("127.0.0.1:5557", grpc.WithTransportCredentials(grpcinsecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	client := dexapi.NewDexClient(conn)

	secret := rand.Text()
	id := "kube-bind-" + port

	_, err = client.CreateClient(t.Context(), &dexapi.CreateClientReq{
		Client: &dexapi.Client{
			Id:           id,
			Secret:       secret,
			RedirectUris: []string{fmt.Sprintf("http://%s/api/callback", addr)},
			Public:       true,
			Name:         "kube-bind on port " + port,
		},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithDeadline(t.Context(), metav1.Now().Add(10*time.Second))
		defer cancel()
		conn, err := grpc.NewClient("127.0.0.1:5557", grpc.WithTransportCredentials(grpcinsecure.NewCredentials()))
		require.NoError(t, err)
		_, err = dexapi.NewDexClient(conn).DeleteClient(ctx, &dexapi.DeleteClientReq{Id: id})
		require.NoError(t, err)
	})

	return id, secret
}
