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
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOIDCCodeFlow drives the full authorization-code + PKCE dance against
// the embedded mock issuer: login → issuer → callback → session cookie, then
// authenticates with both the cookie and the bearer token.
func TestOIDCCodeFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessions := NewSessions(nil, nil, time.Hour)

	// The gateway mux: OIDC routes + a probe endpoint that reports identity.
	mux := http.NewServeMux()
	var oidc *OIDC
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		id, err := oidc.Authenticate(r)
		if err != nil {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(id.Subject))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg, err := StartMockOIDC(ctx, server.URL+"/api/auth/oidc/callback", "")
	require.NoError(t, err)

	oidc, err = NewOIDC(ctx, cfg, sessions)
	require.NoError(t, err)
	oidc.RegisterRoutes(mux)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	browser := &http.Client{Jar: jar}

	// The mock issuer auto-approves as its default user; the whole dance is
	// redirects the "browser" follows.
	loginReq, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/auth/oidc/login?redirect=/", nil)
	require.NoError(t, err)
	res, err := browser.Do(loginReq)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode, "login flow should land back on /")

	// Cookie authenticates.
	whoamiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/whoami", nil)
	require.NoError(t, err)
	res2, err := browser.Do(whoamiReq)
	require.NoError(t, err)
	defer func() { _ = res2.Body.Close() }()
	require.Equal(t, http.StatusOK, res2.StatusCode)

	// The session cookie's value works verbatim as a bearer token (CLI path).
	u, _ := res2.Request.URL.Parse("/")
	var bearer string
	for _, c := range jar.Cookies(u) {
		if c.Name == SessionCookie {
			bearer = c.Value
		}
	}
	require.NotEmpty(t, bearer)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/whoami", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+bearer)
	res3, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res3.Body.Close() }()
	require.Equal(t, http.StatusOK, res3.StatusCode)

	id, err := sessions.Verify(bearer)
	require.NoError(t, err)
	require.Contains(t, id.Subject, cfg.IssuerURL+"#", "subject is issuer-qualified")
	require.Equal(t, "jane.doe@example.com", id.DisplayName)
}

func TestSafeRedirect(t *testing.T) {
	for redirect, want := range map[string]bool{
		"/":                          true,
		"/catalog":                   true,
		"//evil.example.com":         false,
		"https://evil.example.com/x": false,
		"http://localhost:8123/cb":   true,
		"http://127.0.0.1:9999/cb":   true,
		"ftp://localhost/x":          false,
		"":                           false,
	} {
		require.Equal(t, want, safeRedirect(redirect), "redirect %q", redirect)
	}
}
