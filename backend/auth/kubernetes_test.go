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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func kubernetesAuthWithReview(t *testing.T, review func(token string) authenticationv1.TokenReviewStatus) *Kubernetes {
	t.Helper()
	clientset := fake.NewClientset()
	clientset.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr = tr.DeepCopy()
		tr.Status = review(tr.Spec.Token)
		return true, tr, nil
	})
	return NewKubernetesFromClientset(clientset)
}

func TestKubernetesAuthenticator(t *testing.T) {
	k := kubernetesAuthWithReview(t, func(token string) authenticationv1.TokenReviewStatus {
		if token == "good-token" {
			return authenticationv1.TokenReviewStatus{
				Authenticated: true,
				User: authenticationv1.UserInfo{
					Username: "system:serviceaccount:tools:portal",
					Groups:   []string{"system:serviceaccounts"},
				},
			}
		}
		return authenticationv1.TokenReviewStatus{Authenticated: false}
	})

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer good-token")
	id, err := k.Authenticate(r)
	require.NoError(t, err)
	require.Equal(t, "kubernetes#system:serviceaccount:tools:portal", id.Subject,
		"tenancy key is issuer-qualified, stable across re-binds")
	require.Equal(t, []string{"system:serviceaccounts"}, id.Groups)

	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer bad-token")
	_, err = k.Authenticate(r)
	require.ErrorIs(t, err, ErrUnauthenticated)

	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	_, err = k.Authenticate(r)
	require.ErrorIs(t, err, ErrUnauthenticated, "no token at all")
}
