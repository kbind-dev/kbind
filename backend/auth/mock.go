/*
Copyright 2026 The Kbind Authors.

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

package auth

import (
	"context"
	"fmt"
	"net"

	"github.com/oauth2-proxy/mockoidc"
)

// StartMockOIDC runs an embedded mock OIDC issuer and returns an OIDCConfig
// pointing at it. Dev-mode only (the --oidc-mock flag): every login
// auto-approves as the mock's default user. The issuer stops when ctx ends.
//
// listen is the address to bind ("" = 127.0.0.1:<random>). A fixed port
// (e.g. "127.0.0.1:5556") makes the issuer reachable from a host browser
// through a port-forward when the backend runs in-cluster — the issuer URL
// embedded in the discovery document is derived from this address, so the
// same 127.0.0.1:<port> works from inside the pod and from the host.
func StartMockOIDC(ctx context.Context, redirectURL, listen string) (OIDCConfig, error) {
	m, err := startMock(ctx, listen)
	if err != nil {
		return OIDCConfig{}, fmt.Errorf("starting mock OIDC issuer: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = m.Shutdown()
	}()

	cfg := m.Config()
	return OIDCConfig{
		IssuerURL:     cfg.Issuer,
		ClientID:      cfg.ClientID,
		ClientSecret:  cfg.ClientSecret,
		UsernameClaim: "email",
		RedirectURL:   redirectURL,
	}, nil
}

func startMock(ctx context.Context, listen string) (*mockoidc.MockOIDC, error) {
	if listen == "" {
		return mockoidc.Run()
	}
	m, err := mockoidc.NewServer(nil)
	if err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return nil, err
	}
	return m, m.Start(ln, nil)
}
