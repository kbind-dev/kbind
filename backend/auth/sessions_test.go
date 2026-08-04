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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionsRoundTrip(t *testing.T) {
	s := NewSessions(nil, nil, time.Hour)
	id := &Identity{Subject: "https://issuer#alice", DisplayName: "alice@example.com", Groups: []string{"dev"}}

	token, err := s.Issue(id)
	require.NoError(t, err)

	got, err := s.Verify(token)
	require.NoError(t, err)
	require.Equal(t, id, got)
}

func TestSessionsRejectsTamperedAndForeignTokens(t *testing.T) {
	s := NewSessions(nil, nil, time.Hour)
	token, err := s.Issue(&Identity{Subject: "s"})
	require.NoError(t, err)

	_, err = s.Verify(token + "x")
	require.ErrorIs(t, err, ErrUnauthenticated)

	// A token minted with different keys must not verify.
	other := NewSessions(nil, nil, time.Hour)
	foreign, err := other.Issue(&Identity{Subject: "s"})
	require.NoError(t, err)
	_, err = s.Verify(foreign)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestSessionsSharedKeysVerifyAcrossInstances(t *testing.T) {
	sign := []byte("0123456789abcdef0123456789abcdef")
	encrypt := []byte("fedcba9876543210fedcba9876543210")
	a := NewSessions(sign, encrypt, time.Hour)
	b := NewSessions(sign, encrypt, time.Hour)

	token, err := a.Issue(&Identity{Subject: "replica-safe"})
	require.NoError(t, err)
	got, err := b.Verify(token)
	require.NoError(t, err)
	require.Equal(t, "replica-safe", got.Subject)
}

func TestSessionsExpiry(t *testing.T) {
	s := NewSessions(nil, nil, time.Second)
	token, err := s.Issue(&Identity{Subject: "s"})
	require.NoError(t, err)
	// securecookie's max-age granularity is whole seconds; overshoot clearly.
	time.Sleep(2500 * time.Millisecond)
	_, err = s.Verify(token)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestTokenFromRequest(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	require.Empty(t, TokenFromRequest(r))

	r.Header.Set("Authorization", "Bearer abc")
	require.Equal(t, "abc", TokenFromRequest(r))

	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "cookie-token"})
	require.Equal(t, "cookie-token", TokenFromRequest(r))
}
