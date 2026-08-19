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

// Package reaper detects consumers that stopped coming, keyed off the
// heartbeat Lease the konnector maintains in each Grant's boundary namespace.
// Conservative by default: it marks Grants stale (condition + annotation) and
// only revokes/deletes when explicitly enabled.
package reaper

import (
	"context"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kbind/kbind/backend/issuer"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// Reaper periodically sweeps Grants against their boundary namespaces'
// heartbeat Leases.
type Reaper struct {
	// Client is a provider-cluster client.
	Client client.Client
	// Issuer performs the destructive boundary deletion (opt-in).
	Issuer *issuer.KubeIssuer
	// TTL is how long a Lease may be unrenewed (or a Ready Grant may sit
	// without any Lease) before the Grant is considered stale. Default 30m.
	TTL time.Duration
	// Revoke deletes stale Grants (the issuer finalizer removes the
	// credentials). Off by default.
	Revoke bool
	// DeleteBoundary additionally deletes the boundary namespace (cascading
	// the synced objects inside it) when revoking, if no other Grant shares
	// it. Off by default; requires Revoke.
	DeleteBoundary bool
	// Interval is the sweep cadence. Default 1m.
	Interval time.Duration
}

func (r *Reaper) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return 30 * time.Minute
}

func (r *Reaper) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return time.Minute
}

// Start implements manager.Runnable (needs leader election: destructive-ish,
// single sweeper is enough).
func (r *Reaper) Start(ctx context.Context) error {
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.sweep(ctx); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "reaper sweep failed")
			}
		}
	}
}

// NeedLeaderElection makes the manager run the reaper only on the leader.
func (r *Reaper) NeedLeaderElection() bool { return true }

func (r *Reaper) sweep(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("reaper")
	var grants iamv1alpha1.GrantList
	if err := r.Client.List(ctx, &grants); err != nil {
		return err
	}
	for i := range grants.Items {
		grant := &grants.Items[i]
		if grant.DeletionTimestamp != nil || grant.Status.Namespace == "" {
			continue
		}
		if err := r.sweepGrant(ctx, log, grant); err != nil {
			log.Error(err, "sweeping grant", "grant", grant.Name)
		}
	}
	return nil
}

func (r *Reaper) sweepGrant(ctx context.Context, log logr, grant *iamv1alpha1.Grant) error {
	stale, reason, since, err := r.staleness(ctx, grant)
	if err != nil {
		return err
	}

	if !stale {
		// Heartbeat is healthy (or the grant is too young to judge): clear
		// any previous stale marking.
		if grant.Annotations[iamv1alpha1.AnnotationStaleSince] != "" ||
			apimeta.IsStatusConditionTrue(grant.Status.Conditions, iamv1alpha1.ConditionStale) {
			delete(grant.Annotations, iamv1alpha1.AnnotationStaleSince)
			if err := r.Client.Update(ctx, grant); err != nil {
				return err
			}
			apimeta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
				Type: iamv1alpha1.ConditionStale, Status: metav1.ConditionFalse,
				Reason: iamv1alpha1.ReasonAsExpected, Message: "consumer heartbeat is current",
			})
			return r.Client.Status().Update(ctx, grant)
		}
		return nil
	}

	// Mark first, destroy (optionally) after.
	if grant.Annotations[iamv1alpha1.AnnotationStaleSince] == "" {
		if grant.Annotations == nil {
			grant.Annotations = map[string]string{}
		}
		grant.Annotations[iamv1alpha1.AnnotationStaleSince] = since.UTC().Format(time.RFC3339)
		if err := r.Client.Update(ctx, grant); err != nil {
			return err
		}
	}
	if apimeta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
		Type: iamv1alpha1.ConditionStale, Status: metav1.ConditionTrue,
		Reason: reason, Message: "consumer stopped renewing its heartbeat Lease (or never applied the bundle)",
	}) {
		if err := r.Client.Status().Update(ctx, grant); err != nil {
			return err
		}
		log.Info("grant marked stale", "grant", grant.Name, "reason", reason)
	}

	if !r.Revoke {
		return nil
	}
	if r.DeleteBoundary {
		if err := r.Issuer.DeleteBoundary(ctx, grant); err != nil {
			return err
		}
	}
	log.Info("revoking stale grant", "grant", grant.Name, "reason", reason)
	return client.IgnoreNotFound(r.Client.Delete(ctx, grant))
}

// staleness inspects the boundary namespace's heartbeat Leases. The konnector
// annotates each Lease with its Connection name, and the bundle names the
// Connection after the Grant — so a Grant is judged **per-grant** by the
// Leases matching its own name. Fallback for hand-edited bundles (renamed
// Connection): with no matching Lease, any fresh Lease in the boundary keeps
// the Grant alive (conservative), and only a fully silent boundary goes stale.
func (r *Reaper) staleness(ctx context.Context, grant *iamv1alpha1.Grant) (bool, string, time.Time, error) {
	var leases coordinationv1.LeaseList
	if err := r.Client.List(ctx, &leases, client.InNamespace(grant.Status.Namespace),
		client.MatchingLabels{corev1alpha1.LabelManaged: "true"}); err != nil {
		return false, "", time.Time{}, err
	}

	var newestMatching, newestAny time.Time
	for i := range leases.Items {
		lease := &leases.Items[i]
		if lease.Spec.RenewTime == nil {
			continue
		}
		renewed := lease.Spec.RenewTime.Time
		if renewed.After(newestAny) {
			newestAny = renewed
		}
		if lease.Annotations[corev1alpha1.AnnotationConnection] == grant.Name && renewed.After(newestMatching) {
			newestMatching = renewed
		}
	}

	now := time.Now()
	if !newestMatching.IsZero() {
		// Per-grant precision: this Grant's own Connection heartbeats.
		if now.Sub(newestMatching) > r.ttl() {
			return true, iamv1alpha1.ReasonLeaseExpired, newestMatching, nil
		}
		return false, "", time.Time{}, nil
	}
	if !newestAny.IsZero() && now.Sub(newestAny) <= r.ttl() {
		// Someone in this boundary is alive but not under this Grant's name
		// (renamed Connection) — conservative: not stale.
		return false, "", time.Time{}, nil
	}
	if !newestAny.IsZero() {
		return true, iamv1alpha1.ReasonLeaseExpired, newestAny, nil
	}

	ready := apimeta.FindStatusCondition(grant.Status.Conditions, iamv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		return false, "", time.Time{}, nil
	}
	if readySince := ready.LastTransitionTime.Time; now.Sub(readySince) > r.ttl() {
		return true, iamv1alpha1.ReasonNoLease, readySince, nil
	}
	return false, "", time.Time{}, nil
}

// logr is the narrow logging interface the reaper needs.
type logr interface {
	Info(msg string, keysAndValues ...any)
	Error(err error, msg string, keysAndValues ...any)
}
