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

package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig configures the OIDC authenticator, kube-apiserver/kcp flag style.
type OIDCConfig struct {
	// IssuerURL is the OIDC issuer (--oidc-issuer-url).
	IssuerURL string
	// ClientID is the OAuth2 client ID (--oidc-client-id).
	ClientID string
	// ClientSecret is the OAuth2 client secret (--oidc-client-secret). The
	// gateway is a confidential web client doing the code grant (with PKCE).
	ClientSecret string
	// CAFile is a PEM bundle for the issuer's TLS (--oidc-ca-file). Empty =
	// system roots.
	CAFile string
	// UsernameClaim is the ID-token claim used as the stable username
	// (--oidc-username-claim). Default "sub".
	UsernameClaim string
	// GroupsClaim is the ID-token claim holding groups
	// (--oidc-groups-claim). Empty = no groups.
	GroupsClaim string
	// Scopes are the OAuth2 scopes to request (--oidc-scopes). Default
	// "openid,profile,email".
	Scopes []string
	// RedirectURL is the externally reachable callback URL
	// (--oidc-redirect-url), e.g. https://gateway.example.com/api/auth/oidc/callback.
	RedirectURL string
	// InsecureSkipTLSVerify disables TLS verification toward the issuer.
	// Dev-mode only (used by --oidc-mock's self-signed issuer).
	InsecureSkipTLSVerify bool
}

// stateCookie is the short-lived cookie carrying the login flow state.
const stateCookie = "kbind_oidc_state"

// oidcState is the per-login flow state, kept client-side in an encrypted
// cookie (no server state).
type oidcState struct {
	State        string `json:"state"`
	Nonce        string `json:"nonce"`
	PKCEVerifier string `json:"pkceVerifier"`
	Redirect     string `json:"redirect"`
}

// OIDC is the reference Authenticator: authorization-code grant with PKCE.
type OIDC struct {
	cfg      OIDCConfig
	client   *http.Client
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	oauth2   oauth2.Config
	sessions *Sessions
}

// NewOIDC discovers the issuer and builds the OIDC authenticator.
func NewOIDC(ctx context.Context, cfg OIDCConfig, sessions *Sessions) (*OIDC, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, fmt.Errorf("oidc requires --oidc-issuer-url, --oidc-client-id and --oidc-redirect-url")
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = "sub"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}

	httpClient, err := issuerHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	provider, err := gooidc.NewProvider(gooidc.ClientContext(ctx, httpClient), cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discovering OIDC issuer %q: %w", cfg.IssuerURL, err)
	}

	return &OIDC{
		cfg:      cfg,
		client:   httpClient,
		provider: provider,
		verifier: provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       cfg.Scopes,
		},
		sessions: sessions,
	}, nil
}

func issuerHTTPClient(cfg OIDCConfig) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-mode flag, see OIDCConfig
	} else if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading --oidc-ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--oidc-ca-file %q contains no certificates", cfg.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

// Name implements Authenticator.
func (o *OIDC) Name() string { return "oidc" }

// RegisterRoutes mounts the login and callback endpoints.
func (o *OIDC) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/oidc/login", o.handleLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", o.handleCallback)
}

// Authenticate implements Authenticator: session cookie or bearer token.
func (o *OIDC) Authenticate(r *http.Request) (*Identity, error) {
	token := TokenFromRequest(r)
	if token == "" {
		return nil, ErrUnauthenticated
	}
	return o.sessions.Verify(token)
}

// handleLogin starts the code+PKCE flow. ?redirect= says where to land after
// the callback: a local path (UI) or a http://localhost:<port>/... URL (CLI).
func (o *OIDC) handleLogin(w http.ResponseWriter, r *http.Request) {
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	if !safeRedirect(redirect) {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}

	state := oidcState{
		State:        randomToken(16),
		Nonce:        randomToken(16),
		PKCEVerifier: oauth2.GenerateVerifier(),
		Redirect:     redirect,
	}
	encoded, err := o.sessions.codec.Encode(stateCookie, state)
	if err != nil {
		http.Error(w, "encoding state", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    encoded,
		Path:     "/api/auth/oidc/",
		MaxAge:   int((10 * time.Minute).Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, o.oauth2.AuthCodeURL(state.State,
		gooidc.Nonce(state.Nonce),
		oauth2.S256ChallengeOption(state.PKCEVerifier),
	), http.StatusFound)
}

// handleCallback finishes the flow: code exchange, ID-token verification,
// session issuance, redirect.
func (o *OIDC) handleCallback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "missing login state (restart login)", http.StatusBadRequest)
		return
	}
	var state oidcState
	if err := o.sessions.codec.Decode(stateCookie, cookie.Value, &state); err != nil {
		http.Error(w, "invalid login state (restart login)", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != state.State {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// The state cookie is one-shot.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/api/auth/oidc/", MaxAge: -1})

	ctx := gooidc.ClientContext(r.Context(), o.client)
	token, err := o.oauth2.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(state.PKCEVerifier))
	if err != nil {
		http.Error(w, "code exchange failed: "+err.Error(), http.StatusUnauthorized)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "token response has no id_token", http.StatusUnauthorized)
		return
	}
	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		http.Error(w, "id_token verification failed: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if idToken.Nonce != state.Nonce {
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}

	identity, err := o.identityFromClaims(idToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	session, err := o.sessions.Issue(identity)
	if err != nil {
		http.Error(w, "issuing session", http.StatusInternalServerError)
		return
	}
	o.sessions.SetSessionCookie(w, r, session)

	// CLI flow: a localhost redirect gets the token appended so the CLI can
	// store it as its bearer token. UI flow: plain path redirect, cookie only.
	redirect := state.Redirect
	if u, err := url.Parse(redirect); err == nil && u.Host != "" {
		q := u.Query()
		q.Set("token", session)
		u.RawQuery = q.Encode()
		redirect = u.String()
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// identityFromClaims maps ID-token claims to an Identity per the configured
// username/groups claims. Subject is "<issuer>#<username>" — the stable
// tenancy key.
func (o *OIDC) identityFromClaims(idToken *gooidc.IDToken) (*Identity, error) {
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parsing id_token claims: %w", err)
	}
	username, ok := claims[o.cfg.UsernameClaim].(string)
	if !ok || username == "" {
		return nil, fmt.Errorf("id_token has no usable %q claim", o.cfg.UsernameClaim)
	}

	display := username
	for _, k := range []string{"email", "name", "preferred_username"} {
		if v, ok := claims[k].(string); ok && v != "" {
			display = v
			break
		}
	}

	var groups []string
	if o.cfg.GroupsClaim != "" {
		switch v := claims[o.cfg.GroupsClaim].(type) {
		case string:
			groups = []string{v}
		case []any:
			for _, g := range v {
				if s, ok := g.(string); ok {
					groups = append(groups, s)
				}
			}
		}
	}

	return &Identity{
		Subject:     o.cfg.IssuerURL + "#" + username,
		DisplayName: display,
		Groups:      groups,
	}, nil
}

// safeRedirect allows local paths (UI) and localhost URLs (CLI callback), and
// nothing else — the callback must never become an open redirect.
func safeRedirect(redirect string) bool {
	if redirect == "" {
		return false
	}
	if redirect[0] == '/' {
		return len(redirect) == 1 || redirect[1] != '/' // path, not scheme-relative //host
	}
	u, err := url.Parse(redirect)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return (u.Scheme == "http" || u.Scheme == "https") && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
