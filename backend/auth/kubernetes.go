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
	"fmt"
	"net/http"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Kubernetes authenticates bearer tokens with a TokenReview against the
// provider cluster — the second reference Authenticator, for in-platform
// callers (UIs, controllers) that already hold a provider-cluster identity
// and should not need a second SSO round trip. It has no interactive flow:
// the caller simply sends its own token as "Authorization: Bearer".
type Kubernetes struct {
	client kubernetes.Interface
}

// NewKubernetes builds the TokenReview authenticator for the cluster behind
// cfg (the provider).
func NewKubernetes(cfg *rest.Config) (*Kubernetes, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes authenticator client: %w", err)
	}
	return &Kubernetes{client: clientset}, nil
}

// NewKubernetesFromClientset is the injection point for tests.
func NewKubernetesFromClientset(clientset kubernetes.Interface) *Kubernetes {
	return &Kubernetes{client: clientset}
}

// Name implements Authenticator.
func (k *Kubernetes) Name() string { return "kubernetes" }

// RegisterRoutes implements Authenticator: no interactive flow to mount.
func (k *Kubernetes) RegisterRoutes(_ *http.ServeMux) {}

// Authenticate reviews the request's bearer token against the provider. The
// tenancy key is "kubernetes#<username>", so the same cluster identity gets
// the same boundary on re-bind.
func (k *Kubernetes) Authenticate(r *http.Request) (*Identity, error) {
	token := TokenFromRequest(r)
	if token == "" {
		return nil, ErrUnauthenticated
	}
	review, err := k.client.AuthenticationV1().TokenReviews().Create(r.Context(),
		&authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}},
		metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: token review failed: %v", ErrUnauthenticated, err)
	}
	if !review.Status.Authenticated || review.Status.User.Username == "" {
		return nil, ErrUnauthenticated
	}
	return &Identity{
		Subject:     "kubernetes#" + review.Status.User.Username,
		DisplayName: review.Status.User.Username,
		Groups:      review.Status.User.Groups,
	}, nil
}
