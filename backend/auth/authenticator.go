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

// Package auth is the gateway's pluggable authentication layer. An
// Authenticator turns HTTP requests into consumer identities; OIDC is the
// reference implementation, not the contract. Sessions are stateless
// encrypted tokens (cookie or bearer), so any number of gateway replicas
// works with shared keys and no session store.
package auth

import (
	"errors"
	"net/http"
)

// Identity is an authenticated consumer identity. Subject is the stable
// tenancy key ("<issuer>#<subject>" for OIDC): the same human gets the same
// provider-side boundary on re-bind.
type Identity struct {
	Subject     string   `json:"subject"`
	DisplayName string   `json:"displayName,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

// ErrUnauthenticated is returned when a request carries no (valid) identity.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator authenticates gateway requests. Implementations mount their
// interactive flow (if any) under /api/auth/<name>/.
type Authenticator interface {
	// Name identifies the method (e.g. "oidc"), surfaced in /api/provider.
	Name() string
	// RegisterRoutes mounts the authenticator's HTTP flow on mux.
	RegisterRoutes(mux *http.ServeMux)
	// Authenticate extracts the identity from a request, or
	// ErrUnauthenticated.
	Authenticate(r *http.Request) (*Identity, error)
}
