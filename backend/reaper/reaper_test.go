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

package reaper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

const boundary = "kbind-abc123"

func testScheme(t *testing.T) *apimachineryruntime.Scheme {
	t.Helper()
	scheme := apimachineryruntime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(iamv1alpha1.AddToScheme(scheme))
	return scheme
}

func lease(name, connection string, renewedAgo time.Duration) *coordinationv1.Lease {
	renew := metav1.NewMicroTime(time.Now().Add(-renewedAgo))
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   boundary,
			Name:        name,
			Labels:      map[string]string{corev1alpha1.LabelManaged: "true"},
			Annotations: map[string]string{corev1alpha1.AnnotationConnection: connection},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: ptr.To("consumer-uid"),
			RenewTime:      &renew,
		},
	}
}

func grant(name string, readyAgo time.Duration) *iamv1alpha1.Grant {
	return &iamv1alpha1.Grant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: iamv1alpha1.GrantStatus{
			Namespace: boundary,
			Conditions: []metav1.Condition{{
				Type:               iamv1alpha1.ConditionReady,
				Status:             metav1.ConditionTrue,
				Reason:             iamv1alpha1.ReasonAsExpected,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-readyAgo)),
			}},
		},
	}
}

func TestStalenessIsPerGrant(t *testing.T) {
	ttl := time.Minute
	for _, tc := range []struct {
		name       string
		leases     []client.Object
		grant      *iamv1alpha1.Grant
		wantStale  bool
		wantReason string
	}{
		{
			name:      "own lease fresh -> alive",
			leases:    []client.Object{lease("g1-uid", "g1", time.Second)},
			grant:     grant("g1", time.Hour),
			wantStale: false,
		},
		{
			name: "own lease expired -> stale, even though a sibling grant in the same boundary is alive",
			leases: []client.Object{
				lease("g1-uid", "g1", time.Hour),
				lease("g2-uid", "g2", time.Second),
			},
			grant:      grant("g1", time.Hour),
			wantStale:  true,
			wantReason: iamv1alpha1.ReasonLeaseExpired,
		},
		{
			name:      "no matching lease but a fresh foreign one (renamed Connection) -> conservative, alive",
			leases:    []client.Object{lease("renamed-uid", "renamed", time.Second)},
			grant:     grant("g1", time.Hour),
			wantStale: false,
		},
		{
			name:       "no matching lease, all foreign leases expired -> stale",
			leases:     []client.Object{lease("renamed-uid", "renamed", time.Hour)},
			grant:      grant("g1", time.Hour),
			wantStale:  true,
			wantReason: iamv1alpha1.ReasonLeaseExpired,
		},
		{
			name:       "no lease at all, Ready beyond TTL -> stale (bundle never applied)",
			leases:     nil,
			grant:      grant("g1", time.Hour),
			wantStale:  true,
			wantReason: iamv1alpha1.ReasonNoLease,
		},
		{
			name:      "no lease at all, freshly Ready -> not judged yet",
			leases:    nil,
			grant:     grant("g1", time.Second),
			wantStale: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tc.leases...).Build()
			r := &Reaper{Client: c, TTL: ttl}

			stale, reason, _, err := r.staleness(context.Background(), tc.grant)
			require.NoError(t, err)
			require.Equal(t, tc.wantStale, stale)
			if tc.wantStale {
				require.Equal(t, tc.wantReason, reason)
			}
		})
	}
}
