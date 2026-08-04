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

// Package gateway is the kbind backend's HTTP API: catalog browsing, bind,
// one-time bundle pickup, and (flag-gated) browser-apply. It is stateless —
// sessions are encrypted tokens, and the only handshake state (one-time
// pickup) lives on the Grant via annotations — so any number of replicas
// works. One gateway fronts exactly one provider.
package gateway

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kbind/kbind/backend/auth"
	"github.com/kbind/kbind/backend/gateway/api"
	"github.com/kbind/kbind/backend/issuer"
	"github.com/kbind/kbind/pkg/konnectorinstall"
)

// Config is the gateway's static configuration.
type Config struct {
	// ProviderName is the human-facing provider name in /api/provider.
	ProviderName string
	// Version is reported in /api/provider.
	Version string
	// ApplyEnabled turns on the POST /api/apply browser-apply path
	// (--enable-apply). Off by default: consumer credentials transit the
	// gateway when enabled.
	ApplyEnabled bool
	// PickupTTL is how long a bundle pickup URL lives. Default 5m.
	PickupTTL time.Duration
	// BindTimeout is how long POST /api/bind waits for the issuer to
	// provision credentials before telling the caller to retry. Default 30s.
	BindTimeout time.Duration
	// KonnectorImage is the konnector image browser-apply installs.
	KonnectorImage string
}

func (c Config) pickupTTL() time.Duration {
	if c.PickupTTL > 0 {
		return c.PickupTTL
	}
	return 5 * time.Minute
}

func (c Config) bindTimeout() time.Duration {
	if c.BindTimeout > 0 {
		return c.BindTimeout
	}
	return 30 * time.Second
}

// Server is the gateway HTTP server.
type Server struct {
	cfg Config
	// client is a provider-cluster client (catalog, Grants, token Secrets).
	client client.Client
	bundle *issuer.BundleBuilder
	auths  []auth.Authenticator
	mux    *http.ServeMux
}

// New assembles the gateway. ui is the embedded SPA filesystem (nil disables
// the UI routes).
func New(cfg Config, providerClient client.Client, bundle *issuer.BundleBuilder, auths []auth.Authenticator, ui fs.FS) *Server {
	s := &Server{
		cfg:    cfg,
		client: providerClient,
		bundle: bundle,
		auths:  auths,
		mux:    http.NewServeMux(),
	}

	s.mux.HandleFunc("GET /api/provider", s.handleProvider)
	s.mux.HandleFunc("GET /api/me", s.handleMe)
	s.mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	s.mux.HandleFunc("GET /api/catalog/{export}/instances", s.handleExportInstances)
	s.mux.HandleFunc("GET /api/clusters", s.handleClusters)
	s.mux.HandleFunc("POST /api/bind", s.handleBind)
	s.mux.HandleFunc("GET /api/bundle/{token}", s.handleBundle)
	if cfg.ApplyEnabled {
		s.mux.HandleFunc("POST /api/apply", s.handleApply)
	}
	s.mux.HandleFunc("GET /api/konnector", s.handleKonnectorManifests)
	s.mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("GET /api/auth/logout", s.handleLogout)
	for _, a := range s.auths {
		a.RegisterRoutes(s.mux)
	}
	if ui != nil {
		s.mux.Handle("GET /", http.FileServerFS(ui))
		// The login page is separate from the app; the app redirects here
		// when unauthenticated.
		s.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, ui, "login.html")
		})
	}
	return s
}

// handleKonnectorManifests serves the konnector install (CRDs, RBAC,
// Deployment) as one apply-able YAML multi-doc — the "connect a cluster"
// counterpart of the bundle. No auth: it carries no credentials, so
// `curl … | kubectl apply -f -` stays a complete client.
func (s *Server) handleKonnectorManifests(w http.ResponseWriter, _ *http.Request) {
	manifests, err := konnectorinstall.Manifests(s.cfg.KonnectorImage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="konnector.yaml"`)
	_, _ = w.Write(manifests)
}

// handleLogout clears the session cookie and lands on the login page. Bearer
// tokens are stateless and simply expire; logout is the browser story.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// Handler returns the gateway's HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// identity authenticates a request against the configured authenticators.
func (s *Server) identity(r *http.Request) (*auth.Identity, error) {
	for _, a := range s.auths {
		id, err := a.Authenticate(r)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, auth.ErrUnauthenticated) {
			return nil, err
		}
	}
	return nil, auth.ErrUnauthenticated
}

// writeJSON writes v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encoding response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// writeError writes an api.Error.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.Error{Error: msg})
}

// handleMe reports the caller's identity (the UI's sidebar footer).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, id)
}

// handleProvider serves provider metadata + auth methods (no auth required).
func (s *Server) handleProvider(w http.ResponseWriter, _ *http.Request) {
	methods := make([]string, 0, len(s.auths))
	for _, a := range s.auths {
		methods = append(methods, a.Name())
	}
	writeJSON(w, http.StatusOK, api.Provider{
		Name:         s.cfg.ProviderName,
		Version:      s.cfg.Version,
		AuthMethods:  methods,
		ApplyEnabled: s.cfg.ApplyEnabled,
	})
}
