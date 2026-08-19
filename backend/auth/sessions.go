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
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/securecookie"
)

// SessionCookie is the cookie carrying the encrypted session token. The same
// token verbatim is accepted as "Authorization: Bearer ..." (the CLI path).
const SessionCookie = "kbind_session"

// Sessions encodes identities into stateless encrypted tokens and back. With
// shared keys, every gateway replica can verify every token — no store.
type Sessions struct {
	codec *securecookie.SecureCookie
	ttl   time.Duration
}

// NewSessions builds a Sessions codec. signingKey (32 or 64 bytes) and
// encryptionKey (16, 24 or 32 bytes) must be shared across replicas; pass nil
// to generate ephemeral random keys (dev only — sessions die with the
// process).
func NewSessions(signingKey, encryptionKey []byte, ttl time.Duration) *Sessions {
	if len(signingKey) == 0 {
		signingKey = randomKey(64)
	}
	if len(encryptionKey) == 0 {
		encryptionKey = randomKey(32)
	}
	codec := securecookie.New(signingKey, encryptionKey)
	codec.MaxAge(int(ttl.Seconds()))
	return &Sessions{codec: codec, ttl: ttl}
}

// TTL is the session lifetime tokens are issued with.
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Issue encodes an identity into a session token.
func (s *Sessions) Issue(id *Identity) (string, error) {
	return s.codec.Encode(SessionCookie, id)
}

// Verify decodes and validates a session token.
func (s *Sessions) Verify(token string) (*Identity, error) {
	var id Identity
	if err := s.codec.Decode(SessionCookie, token, &id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if id.Subject == "" {
		return nil, ErrUnauthenticated
	}
	return &id, nil
}

// TokenFromRequest extracts the session token from the Authorization header
// (Bearer) or the session cookie. Empty if neither is present.
func TokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if token, ok := strings.CutPrefix(h, "Bearer "); ok {
			return token
		}
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// SetSessionCookie writes the session token as a cookie.
func (s *Sessions) SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.ttl.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func randomKey(n int) []byte {
	key := make([]byte, n)
	if _, err := rand.Read(key); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return key
}
