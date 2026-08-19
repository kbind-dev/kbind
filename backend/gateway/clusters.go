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

package gateway

import (
	"context"
	"net/http"
	"sort"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kbind/kbind/backend/gateway/api"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// heartbeatGrace is how many missed lease durations still count as live.
const heartbeatGrace = 2

// handleClusters aggregates the konnector heartbeat Leases into a
// consumer-cluster view: each Lease is annotated with its Connection name
// (== the Grant name, since the bundle names the Connection after the Grant)
// and the consumer cluster UID — which cluster consumes which grant, how
// fresh its heartbeat is, and how many objects it has synced per API.
func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	if _, err := s.identity(r); err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	ctx := r.Context()

	var grants iamv1alpha1.GrantList
	if err := s.client.List(ctx, &grants); err != nil {
		writeError(w, http.StatusBadGateway, "listing grants: "+err.Error())
		return
	}

	out := api.Clusters{Clusters: []api.Cluster{}}
	clusters := map[string]*api.Cluster{}
	leaseCache := map[string][]coordinationv1.Lease{}
	// (api name, cluster UID) -> synced object count, filled lazily per API.
	counts := map[string]map[string]int{}

	for i := range grants.Items {
		grant := &grants.Items[i]
		if grant.DeletionTimestamp != nil {
			continue
		}

		leases, err := s.boundaryLeases(ctx, leaseCache, grant.Status.Namespace)
		if err != nil {
			writeError(w, http.StatusBadGateway, "listing heartbeat leases: "+err.Error())
			return
		}
		matched := false
		for j := range leases {
			lease := &leases[j]
			if lease.Annotations[corev1alpha1.AnnotationConnection] != grant.Name {
				continue
			}
			uid := lease.Annotations[corev1alpha1.AnnotationConsumerClusterUID]
			if uid == "" || lease.Spec.RenewTime == nil {
				continue
			}
			matched = true

			binding := api.ClusterGrant{
				Grant:             grant.Name,
				Export:            grant.Spec.ExportName,
				Identity:          grant.Spec.Identity.DisplayName,
				Subject:           grant.Spec.Identity.Subject,
				BoundaryNamespace: grant.Status.Namespace,
				LastHeartbeat:     lease.Spec.RenewTime.Time.UTC(),
				Live:              leaseLive(lease),
			}
			for _, ref := range grant.Spec.APIs {
				perCluster, err := s.syncedCounts(ctx, counts, ref.Name)
				if err != nil {
					// Best-effort: the view is about liveness first.
					perCluster = nil
				}
				binding.APIs = append(binding.APIs, api.SyncedAPI{
					Name:        ref.Name,
					SyncedCount: perCluster[uid],
				})
			}

			cluster, ok := clusters[uid]
			if !ok {
				cluster = &api.Cluster{UID: uid}
				clusters[uid] = cluster
			}
			cluster.Bindings = append(cluster.Bindings, binding)
			if binding.LastHeartbeat.After(cluster.LastHeartbeat) {
				cluster.LastHeartbeat = binding.LastHeartbeat
			}
			cluster.Live = cluster.Live || binding.Live
		}

		if !matched {
			out.Pending = append(out.Pending, api.PendingGrant{
				Grant:     grant.Name,
				Export:    grant.Spec.ExportName,
				Identity:  grant.Spec.Identity.DisplayName,
				CreatedAt: grant.CreationTimestamp.Time.UTC(),
			})
		}
	}

	for _, cluster := range clusters {
		sort.Slice(cluster.Bindings, func(a, b int) bool { return cluster.Bindings[a].Grant < cluster.Bindings[b].Grant })
		out.Clusters = append(out.Clusters, *cluster)
	}
	sort.Slice(out.Clusters, func(a, b int) bool { return out.Clusters[a].UID < out.Clusters[b].UID })
	sort.Slice(out.Pending, func(a, b int) bool { return out.Pending[a].Grant < out.Pending[b].Grant })

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) boundaryLeases(ctx context.Context, cache map[string][]coordinationv1.Lease, namespace string) ([]coordinationv1.Lease, error) {
	if namespace == "" {
		return nil, nil
	}
	if leases, ok := cache[namespace]; ok {
		return leases, nil
	}
	var list coordinationv1.LeaseList
	if err := s.client.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingLabels{corev1alpha1.LabelManaged: "true"}); err != nil {
		return nil, err
	}
	cache[namespace] = list.Items
	return list.Items, nil
}

func leaseLive(lease *coordinationv1.Lease) bool {
	duration := 60 * time.Second
	if lease.Spec.LeaseDurationSeconds != nil {
		duration = time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
	}
	return time.Since(lease.Spec.RenewTime.Time) <= heartbeatGrace*duration
}

// syncedCounts counts, per consumer cluster UID, the provider objects of one
// bound API that the sync engine wrote (managed label + consumer-cluster-uid
// marker). One list per API per request, cached in counts.
func (s *Server) syncedCounts(ctx context.Context, counts map[string]map[string]int, apiName string) (map[string]int, error) {
	if perCluster, ok := counts[apiName]; ok {
		return perCluster, nil
	}
	items, err := s.listManaged(ctx, apiName)
	if err != nil {
		return nil, err
	}
	perCluster := map[string]int{}
	for i := range items {
		if uid := items[i].GetAnnotations()[corev1alpha1.AnnotationConsumerClusterUID]; uid != "" {
			perCluster[uid]++
		}
	}
	counts[apiName] = perCluster
	return perCluster, nil
}
