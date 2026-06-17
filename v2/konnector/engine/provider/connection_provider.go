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

// Package provider implements the multicluster-runtime provider that turns each
// ready Connection into an engaged provider cluster. This is the consumer<->provider
// bridge from the v2 proposal: one Connection == one logical provider cluster.
package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/kbind/kbind/v2/konnector/engine/remote"
	corev1alpha1 "github.com/kbind/kbind/v2/sdk/apis/core/v1alpha1"
)

var _ multicluster.Provider = &ConnectionProvider{}

// Options configures the ConnectionProvider.
type Options struct {
	// ClusterOptions are passed to the cluster constructor.
	ClusterOptions []cluster.Option
	// NewCluster builds (but does not start) a cluster from a rest.Config.
	NewCluster func(ctx context.Context, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error)
}

func setDefaults(o *Options) {
	if o.NewCluster == nil {
		o.NewCluster = func(_ context.Context, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error) {
			return cluster.New(cfg, opts...)
		}
	}
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// ConnectionProvider discovers provider clusters from Connection objects and
// engages them with the multicluster manager. The cluster key is the
// Connection name.
type ConnectionProvider struct {
	opts     Options
	hcClient client.Client

	mcMgr mcmanager.Manager

	lock      sync.Mutex
	clusters  map[string]cluster.Cluster
	cancelFns map[string]context.CancelFunc
	indexers  []index
}

// New registers the provider as a controller on the local (consumer) manager,
// watching Connection objects.
func New(localMgr manager.Manager, opts Options) (*ConnectionProvider, error) {
	p := &ConnectionProvider{
		opts:      opts,
		hcClient:  localMgr.GetClient(),
		clusters:  map[string]cluster.Cluster{},
		cancelFns: map[string]context.CancelFunc{},
	}
	setDefaults(&p.opts)

	if err := builder.ControllerManagedBy(localMgr).
		For(&corev1alpha1.Connection{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Named("connection-provider").
		Complete(p); err != nil {
		return nil, fmt.Errorf("creating connection-provider controller: %w", err)
	}
	return p, nil
}

// Reconcile engages a provider cluster once its Connection is Ready, and
// disengages it on deletion.
func (p *ConnectionProvider) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("connection", req.Name)
	key := req.Name

	conn := &corev1alpha1.Connection{}
	if err := p.hcClient.Get(ctx, req.NamespacedName, conn); err != nil {
		if apierrors.IsNotFound(err) {
			p.disengage(key)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting Connection: %w", err)
	}

	p.lock.Lock()
	mcMgr := p.mcMgr
	_, engaged := p.clusters[key]
	p.lock.Unlock()

	if mcMgr == nil {
		return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
	}
	// A Connection that is no longer Ready (e.g. its credential was revoked, the
	// provider became unreachable, or RBAC was withdrawn) must be disengaged, not
	// just left running against a dead cluster. This also covers a Connection
	// mid-deletion that has already flipped not-Ready.
	if !isReady(conn) {
		if engaged {
			p.disengage(key)
			log.Info("Disengaged provider cluster (Connection no longer ready)")
		} else {
			log.V(4).Info("Connection not ready yet, not engaging")
		}
		return reconcile.Result{}, nil
	}
	if engaged {
		return reconcile.Result{}, nil
	}

	p.lock.Lock()
	defer p.lock.Unlock()
	// Re-check under the lock in case a concurrent reconcile engaged it.
	if _, ok := p.clusters[key]; ok {
		return reconcile.Result{}, nil
	}

	cfg, err := remote.RestConfigFromConnection(ctx, p.hcClient, conn)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("building provider rest config: %w", err)
	}

	cl, err := p.opts.NewCluster(ctx, cfg, p.opts.ClusterOptions...)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("creating provider cluster: %w", err)
	}
	for _, idx := range p.indexers {
		if err := cl.GetCache().IndexField(ctx, idx.object, idx.field, idx.extractValue); err != nil {
			return reconcile.Result{}, fmt.Errorf("indexing field %q: %w", idx.field, err)
		}
	}

	clusterCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := cl.Start(clusterCtx); err != nil {
			log.Error(err, "provider cluster stopped")
		}
	}()
	if !cl.GetCache().WaitForCacheSync(clusterCtx) {
		cancel()
		return reconcile.Result{}, fmt.Errorf("provider cluster cache failed to sync")
	}

	p.clusters[key] = cl
	p.cancelFns[key] = cancel

	if err := p.mcMgr.Engage(clusterCtx, key, cl); err != nil {
		cancel()
		delete(p.clusters, key)
		delete(p.cancelFns, key)
		return reconcile.Result{}, fmt.Errorf("engaging provider cluster: %w", err)
	}
	log.Info("Engaged provider cluster")
	return reconcile.Result{}, nil
}

func (p *ConnectionProvider) disengage(key string) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if cancel, ok := p.cancelFns[key]; ok {
		cancel()
		delete(p.cancelFns, key)
	}
	delete(p.clusters, key)
}

// Get returns the engaged cluster for the given Connection name.
func (p *ConnectionProvider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if cl, ok := p.clusters[clusterName]; ok {
		return cl, nil
	}
	return nil, multicluster.ErrClusterNotFound
}

// Run records the multicluster manager and blocks until the context is done.
func (p *ConnectionProvider) Run(ctx context.Context, mgr mcmanager.Manager) error {
	p.lock.Lock()
	p.mcMgr = mgr
	p.lock.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// IndexField indexes a field on all engaged and future provider clusters.
func (p *ConnectionProvider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.indexers = append(p.indexers, index{object: obj, field: field, extractValue: extractValue})
	for name, cl := range p.clusters {
		if err := cl.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("indexing field %q on cluster %q: %w", field, name, err)
		}
	}
	return nil
}

func isReady(conn *corev1alpha1.Connection) bool {
	return apimeta.IsStatusConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionReady)
}
