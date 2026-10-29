/*
Copyright 2026 The Kube Bind Authors.

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

package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kube-bind/kube-bind/backend/options"
)

func TestHealthzEndpoints(t *testing.T) {
	server, err := NewServer(options.NewServe())
	require.NoError(t, err)

	for _, path := range []string{"/healthz", "/api/healthz"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.Router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "ok", rec.Body.String())
			require.Equal(t, noCacheHeaders["Cache-Control"], rec.Header().Get("Cache-Control"))
		})
	}
}
