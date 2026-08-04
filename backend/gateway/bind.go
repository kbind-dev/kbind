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

package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	"github.com/kbind/kbind/backend/auth"
	"github.com/kbind/kbind/backend/gateway/api"
	"github.com/kbind/kbind/backend/issuer"
	catalogv1alpha1 "github.com/kbind/kbind/sdk/apis/catalog/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// errGone marks a pickup token that is used, expired or unknown.
var errGone = errors.New("bundle pickup token is invalid, expired or already used")

// handleBind drives the issuer for the caller's identity and answers with a
// one-time pickup URL. There is no other handshake state: no request objects
// to poll, no phases.
func (s *Server) handleBind(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req api.BindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Export == "" {
		writeError(w, http.StatusBadRequest, "body must be {\"export\": \"<name>\"}")
		return
	}

	grant, err := s.ensureGrant(r.Context(), id, req.Export)
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "unknown export "+req.Export)
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	if err := s.waitGrantReady(r.Context(), grant); err != nil {
		writeError(w, http.StatusGatewayTimeout, "credentials not provisioned yet (is the issuer running?): "+err.Error())
		return
	}

	token, expiry, err := s.mintPickup(r.Context(), grant)
	if err != nil {
		writeError(w, http.StatusBadGateway, "minting pickup token: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, api.BindResponse{
		Grant:     grant.Name,
		PickupURL: "/api/bundle/" + token,
		ExpiresAt: expiry,
	})
}

// ensureGrant creates or updates the Grant for (identity, export), resolving
// the export's APIs and defaults into the spec so issuance is a stable record.
func (s *Server) ensureGrant(ctx context.Context, id *auth.Identity, exportName string) (*iamv1alpha1.Grant, error) {
	export := &catalogv1alpha1.Export{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: exportName}, export); err != nil {
		return nil, err
	}
	if !apimeta.IsStatusConditionPresentAndEqual(export.Status.Conditions, catalogv1alpha1.ConditionReady, metav1.ConditionTrue) {
		return nil, fmt.Errorf("export %s is not ready (its APIs are not all exported)", exportName)
	}

	grant := &iamv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{Name: issuer.GrantName(exportName, id.Subject)}}
	_, err := controllerutil.CreateOrUpdate(ctx, s.client, grant, func() error {
		grant.Spec = iamv1alpha1.GrantSpec{
			Identity: iamv1alpha1.Identity{
				Subject:     id.Subject,
				DisplayName: id.DisplayName,
				Groups:      id.Groups,
			},
			ExportName:       exportName,
			APIs:             export.Spec.APIs,
			ConflictPolicy:   export.Spec.Defaults.ConflictPolicy,
			RelatedResources: export.Spec.Defaults.RelatedResources,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recording grant: %w", err)
	}
	return grant, nil
}

// waitGrantReady polls until the issuer reports provisioned credentials.
func (s *Server) waitGrantReady(ctx context.Context, grant *iamv1alpha1.Grant) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.bindTimeout())
	defer cancel()
	return wait.PollUntilContextCancel(ctx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		if err := s.client.Get(ctx, types.NamespacedName{Name: grant.Name}, grant); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return apimeta.IsStatusConditionPresentAndEqual(grant.Status.Conditions, iamv1alpha1.ConditionReady, metav1.ConditionTrue), nil
	})
}

// mintPickup stamps a fresh one-time pickup token on the Grant. The token is
// "<base64url(grant)>.<random>"; only sha256(random) is stored, as an
// annotation, so the gateway keeps zero state and any replica can serve the
// pickup. Re-binding replaces the previous (possibly unused) pickup.
func (s *Server) mintPickup(ctx context.Context, grant *iamv1alpha1.Grant) (string, time.Time, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", time.Time{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(secret))
	expiry := time.Now().Add(s.cfg.pickupTTL()).UTC().Truncate(time.Second)

	err := retryConflict(func() error {
		if err := s.client.Get(ctx, types.NamespacedName{Name: grant.Name}, grant); err != nil {
			return err
		}
		if grant.Annotations == nil {
			grant.Annotations = map[string]string{}
		}
		grant.Annotations[iamv1alpha1.AnnotationPickupSHA256] = hex.EncodeToString(sum[:])
		grant.Annotations[iamv1alpha1.AnnotationPickupExpiry] = expiry.Format(time.RFC3339)
		return s.client.Update(ctx, grant)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(grant.Name)) + "." + secret
	return token, expiry, nil
}

// handleBundle is the single-use, short-TTL bundle pickup. The token is the
// auth; no session is required (curl-able, and the CLI redeems it directly).
// Content negotiation: application/json wraps the objects in {"bundle": [...]},
// anything else gets the literal YAML multi-doc.
func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	grant, err := s.consumePickup(r.Context(), r.PathValue("token"))
	if errors.Is(err, errGone) {
		writeError(w, http.StatusGone, errGone.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	kubeconfig, err := s.bundle.Kubeconfig(r.Context(), grant)
	if err != nil {
		writeError(w, http.StatusBadGateway, "assembling kubeconfig: "+err.Error())
		return
	}
	objs := s.bundle.Objects(grant, kubeconfig)

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		out := api.Bundle{}
		for _, obj := range objs {
			raw, err := json.Marshal(obj)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out.Bundle = append(out.Bundle, raw)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", grant.Name+".yaml"))
	for i, obj := range objs {
		data, err := yaml.Marshal(obj)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if i > 0 {
			_, _ = w.Write([]byte("---\n"))
		}
		_, _ = w.Write(data)
	}
}

// consumePickup validates a pickup token and removes it from the Grant with
// optimistic concurrency — the API server enforces single-use, so two racing
// pickups (even on different gateway replicas) redeem exactly once.
func (s *Server) consumePickup(ctx context.Context, token string) (*iamv1alpha1.Grant, error) {
	encodedName, secret, ok := strings.Cut(token, ".")
	if !ok {
		return nil, errGone
	}
	nameBytes, err := base64.RawURLEncoding.DecodeString(encodedName)
	if err != nil {
		return nil, errGone
	}
	sum := sha256.Sum256([]byte(secret))
	want := hex.EncodeToString(sum[:])

	grant := &iamv1alpha1.Grant{}
	err = retryConflict(func() error {
		if err := s.client.Get(ctx, types.NamespacedName{Name: string(nameBytes)}, grant); err != nil {
			if apierrors.IsNotFound(err) {
				return errGone
			}
			return err
		}
		stored := grant.Annotations[iamv1alpha1.AnnotationPickupSHA256]
		expiry, timeErr := time.Parse(time.RFC3339, grant.Annotations[iamv1alpha1.AnnotationPickupExpiry])
		if stored == "" || stored != want || timeErr != nil || time.Now().After(expiry) {
			return errGone
		}
		delete(grant.Annotations, iamv1alpha1.AnnotationPickupSHA256)
		delete(grant.Annotations, iamv1alpha1.AnnotationPickupExpiry)
		return s.client.Update(ctx, grant)
	})
	if err != nil {
		return nil, err
	}
	return grant, nil
}

// retryConflict retries fn on optimistic-concurrency conflicts.
func retryConflict(fn func() error) error {
	var err error
	for range 5 {
		if err = fn(); !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}
