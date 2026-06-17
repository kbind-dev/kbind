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

// Package sync implements the dynamic per-GVR instance sync. A CRD controller
// watches the CRDs installed by bindings and starts/stops a spec/status syncer
// for each.
package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	dynamicclient "k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kbind/kbind/engine/mapper"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

const cleanupTimeout = 10 * time.Second

// ClusterGetter resolves a Connection name to its engaged provider cluster.
// The multicluster-runtime ConnectionProvider satisfies it.
type ClusterGetter interface {
	Get(ctx context.Context, name string) (cluster.Cluster, error)
}

// CRDController watches managed CRDs and runs a per-GVR syncer for each, wired
// to the provider's multicluster-runtime engaged cluster.
type CRDController struct {
	mgr            ctrl.Manager
	consumerConfig *rest.Config
	consumerClient client.Client
	clusters       ClusterGetter
	recorder       record.EventRecorder
	resync         time.Duration
	// mapper translates consumer<->provider object keys. Defaults to
	// mapper.Identity; an out-of-tree build swaps it via WithMapper.
	mapper mapper.Mapper

	lock     sync.Mutex
	contexts map[string]*syncContext // by CRD name
}

// Option configures the CRDController at setup. It is the compile-time seam for
// out-of-tree konnector builds (e.g. a custom Mapper).
type Option func(*CRDController)

// WithMapper overrides the consumer<->provider key Mapper (default: Identity).
func WithMapper(m mapper.Mapper) Option {
	return func(c *CRDController) { c.mapper = m }
}

type syncContext struct {
	generation int64
	// cluster is the engaged provider cluster this syncer was built against. A
	// different instance means the Connection re-engaged and the syncer must be
	// rebuilt (the old one holds a dead client/cache).
	cluster cluster.Cluster
	cancel  context.CancelFunc
	wg      *sync.WaitGroup
}

// SetupWithManager registers the CRD controller. The consumer side uses a
// direct (uncached) client built from the manager's rest config; the provider
// side comes from the multicluster-runtime engaged cluster resolved via clusters.
func SetupWithManager(mgr ctrl.Manager, clusters ClusterGetter, opts ...Option) error {
	consumerClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return fmt.Errorf("building direct consumer client: %w", err)
	}
	r := &CRDController{
		mgr:            mgr,
		consumerConfig: mgr.GetConfig(),
		consumerClient: consumerClient,
		clusters:       clusters,
		recorder:       mgr.GetEventRecorderFor("kbind-konnector"),
		resync:         time.Minute,
		mapper:         mapper.Identity{},
		contexts:       map[string]*syncContext{},
	}
	for _, o := range opts {
		o(r)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiextensionsv1.CustomResourceDefinition{}, builder.WithPredicates(predicate.NewPredicateFuncs(managedCRD))).
		// Watch Connections so an engage/disengage (Ready flip) re-evaluates the
		// syncers of every CRD that Connection pulled — stopping them on disengage
		// and rebuilding them against the freshly-engaged cluster on re-engage.
		Watches(&corev1alpha1.Connection{}, handler.EnqueueRequestsFromMapFunc(r.crdsForConnection)).
		Named("managed-crds").
		Complete(r)
}

func managedCRD(o client.Object) bool {
	return o.GetLabels()[corev1alpha1.LabelManaged] == "true"
}

// crdsForConnection enqueues every managed CRD pulled through the given
// Connection, so its readiness transitions re-evaluate the per-GVR syncers.
func (r *CRDController) crdsForConnection(ctx context.Context, conn client.Object) []reconcile.Request {
	var crds apiextensionsv1.CustomResourceDefinitionList
	if err := r.consumerClient.List(ctx, &crds, client.MatchingLabels{corev1alpha1.LabelManaged: "true"}); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range crds.Items {
		if crds.Items[i].Annotations[corev1alpha1.AnnotationConnection] == conn.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: crds.Items[i].Name}})
		}
	}
	return reqs
}

// Reconcile starts or stops the syncer for a managed CRD.
func (r *CRDController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := r.consumerClient.Get(ctx, req.NamespacedName, crd); err != nil {
		if apierrors.IsNotFound(err) {
			crd = nil
		} else {
			return reconcile.Result{}, err
		}
	}
	return r.reconcile(ctx, req.Name, crd)
}

func (r *CRDController) reconcile(ctx context.Context, name string, crd *apiextensionsv1.CustomResourceDefinition) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("crd", name)

	// The CRD is gone (unbind removed it) — stop the syncer.
	if crd == nil || crd.DeletionTimestamp != nil {
		r.stopSyncer(name, log)
		return reconcile.Result{}, nil
	}
	generation := crd.Generation

	// The provider is pinned by the binding that pulled this CRD.
	connName := crd.Annotations[corev1alpha1.AnnotationConnection]
	if connName == "" {
		return reconcile.Result{}, fmt.Errorf("managed CRD %s has no %s annotation", name, corev1alpha1.AnnotationConnection)
	}

	// The syncer must only run while the Connection is Ready (engaged). If the
	// Connection is gone or no longer Ready, stop the syncer; the Connection watch
	// re-triggers this reconcile when it becomes Ready again.
	conn := &corev1alpha1.Connection{}
	if err := r.consumerClient.Get(ctx, client.ObjectKey{Name: connName}, conn); err != nil {
		if apierrors.IsNotFound(err) {
			r.stopSyncer(name, log)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting Connection %s: %w", connName, err)
	}
	if !connectionReady(conn) || conn.Status.LocalClusterUID == "" {
		r.stopSyncer(name, log)
		log.V(4).Info("provider not ready; syncer stopped", "connection", connName)
		return reconcile.Result{}, nil
	}

	// Resolve the multicluster-runtime engaged provider cluster. A Ready
	// Connection engages asynchronously, so it may be briefly absent — stop any
	// stale syncer and requeue.
	providerCluster, err := r.clusters.Get(ctx, connName)
	if err != nil {
		r.stopSyncer(name, log)
		log.V(4).Info("provider cluster not engaged yet, requeueing", "connection", connName, "reason", err.Error())
		return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Already syncing this generation against this exact engaged cluster? Done.
	// A different cluster instance means the Connection re-engaged (after a
	// disengage) — fall through to rebuild so the syncer tracks the live cluster.
	r.lock.Lock()
	if c, found := r.contexts[name]; found && c.generation >= generation && c.cluster == providerCluster {
		r.lock.Unlock()
		return reconcile.Result{}, nil
	}
	r.lock.Unlock()

	// (Re)build the syncer. Stop any prior one (older generation or dead cluster).
	r.stopSyncer(name, log)

	version := storageOrServedVersion(crd)
	if version == "" {
		return reconcile.Result{}, fmt.Errorf("CRD %s has no served version", name)
	}
	gvr := schema.GroupVersionResource{Group: crd.Spec.Group, Version: version, Resource: crd.Spec.Names.Plural}
	gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: version, Kind: crd.Spec.Names.Kind}

	dyn, err := dynamicclient.NewForConfig(r.consumerConfig)
	if err != nil {
		return reconcile.Result{}, err
	}
	inf := dynamicinformer.NewFilteredDynamicInformer(dyn, gvr, metav1.NamespaceAll, r.resync, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, nil)
	lister := dynamiclister.New(inf.Informer().GetIndexer(), gvr)

	rec := &specReconciler{
		gvr:            gvr,
		gvk:            gvk,
		scope:          crd.Spec.Scope,
		consumerLister: lister,
		consumerClient: r.consumerClient,
		providerClient: providerCluster.GetClient(),
		providerReader: providerCluster.GetAPIReader(),
		localUID:       conn.Status.LocalClusterUID,
		mapper:         r.mapper,
		recorder:       r.recorder,
	}

	syncLog := ctrl.Log.WithName("sync").WithValues("gvr", gvr.String())
	ctrlr, err := controller.NewTypedUnmanaged(fmt.Sprintf("sync-%s", gvr.GroupResource().String()), controller.TypedOptions[reconcile.Request]{
		Reconciler:     rec,
		LogConstructor: func(*reconcile.Request) logr.Logger { return syncLog },
		// A CRD can be removed and re-added (same GVR), recreating this
		// controller with the same name — skip the global uniqueness check.
		SkipNameValidation: ptr.To(true),
	})
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("creating sync controller: %w", err)
	}

	src := newInformerSource(inf)
	if err := ctrlr.Watch(src); err != nil {
		return reconcile.Result{}, fmt.Errorf("watching consumer informer: %w", err)
	}
	// Provider-side watch via the engaged cluster's cache: status/drift events
	// arrive here instead of being polled.
	if err := ctrlr.Watch(providerSource(providerCluster.GetCache(), gvr, gvk, conn.Status.LocalClusterUID, r.mapper)); err != nil {
		return reconcile.Result{}, fmt.Errorf("watching provider cluster: %w", err)
	}
	if err := ctrlr.Watch(r.bindingSource(gvr.GroupResource(), lister)); err != nil {
		return reconcile.Result{}, fmt.Errorf("watching cluster bindings: %w", err)
	}
	if err := ctrlr.Watch(r.namespacedBindingSource(gvr.GroupResource(), lister)); err != nil {
		return reconcile.Result{}, fmt.Errorf("watching bindings: %w", err)
	}

	syncCtx, cancel := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func() { defer wg.Done(); inf.Informer().Run(syncCtx.Done()) }()
	go func() {
		defer wg.Done()
		if err := ctrlr.Start(syncCtx); err != nil {
			runtime.HandleErrorWithContext(syncCtx, err, "sync controller stopped")
		}
	}()
	if !cache.WaitForCacheSync(syncCtx.Done(), inf.Informer().HasSynced) {
		cancel()
		return reconcile.Result{}, fmt.Errorf("consumer informer for %s failed to sync", gvr)
	}

	r.lock.Lock()
	r.contexts[name] = &syncContext{generation: generation, cluster: providerCluster, cancel: cancel, wg: wg}
	r.lock.Unlock()
	log.Info("started syncer", "gvr", gvr.String())
	return reconcile.Result{}, nil
}

func (r *CRDController) bindingSource(gr schema.GroupResource, lister dynamiclister.Lister) source.TypedSource[reconcile.Request] {
	return source.Kind(r.mgr.GetCache(), &corev1alpha1.ClusterBinding{}, handler.TypedEnqueueRequestsFromMapFunc(
		func(_ context.Context, cb *corev1alpha1.ClusterBinding) []reconcile.Request {
			if !listsAPI(cb.Spec.APIs, gr.String()) {
				return nil
			}
			return requestsForAll(lister, "")
		}))
}

func (r *CRDController) namespacedBindingSource(gr schema.GroupResource, lister dynamiclister.Lister) source.TypedSource[reconcile.Request] {
	return source.Kind(r.mgr.GetCache(), &corev1alpha1.Binding{}, handler.TypedEnqueueRequestsFromMapFunc(
		func(_ context.Context, b *corev1alpha1.Binding) []reconcile.Request {
			if !listsAPI(b.Spec.APIs, gr.String()) {
				return nil
			}
			return requestsForAll(lister, b.GetNamespace())
		}))
}

func requestsForAll(lister dynamiclister.Lister, namespace string) []reconcile.Request {
	var list []any
	if namespace != "" {
		l, err := lister.Namespace(namespace).List(labels.Everything())
		if err != nil {
			return nil
		}
		for _, o := range l {
			list = append(list, o)
		}
	} else {
		l, err := lister.List(labels.Everything())
		if err != nil {
			return nil
		}
		for _, o := range l {
			list = append(list, o)
		}
	}
	out := make([]reconcile.Request, 0, len(list))
	for _, o := range list {
		acc, ok := o.(interface {
			GetName() string
			GetNamespace() string
		})
		if !ok {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: acc.GetNamespace(), Name: acc.GetName()}})
	}
	return out
}

func storageOrServedVersion(crd *apiextensionsv1.CustomResourceDefinition) string {
	var served string
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			return v.Name
		}
		if served == "" && v.Served {
			served = v.Name
		}
	}
	return served
}

// stopSyncer tears down the running syncer for a CRD, if any, and blocks (up to
// cleanupTimeout) for its goroutines to exit. A no-op when nothing is running.
func (r *CRDController) stopSyncer(name string, log logr.Logger) {
	r.lock.Lock()
	c, found := r.contexts[name]
	if found {
		delete(r.contexts, name)
	}
	r.lock.Unlock()
	if !found {
		return
	}
	log.Info("stopping syncer")
	c.cancel()
	waitWithTimeout(c.wg, cleanupTimeout)
}

func connectionReady(conn *corev1alpha1.Connection) bool {
	return apimeta.IsStatusConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionReady)
}

func waitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// newInformerSource adapts a dynamic informer to a controller-runtime source.
func newInformerSource(gi interface {
	Informer() cache.SharedIndexInformer
}) source.TypedSource[reconcile.Request] {
	return &informerSourceImpl{informer: gi.Informer()}
}

type informerSourceImpl struct {
	informer cache.SharedIndexInformer
}

func (s *informerSourceImpl) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	_, err := s.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { enqueue(o, q) },
		UpdateFunc: func(_, o any) { enqueue(o, q) },
		DeleteFunc: func(o any) { enqueue(o, q) },
	})
	return err
}

func enqueue(o any, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(o)
	if err != nil {
		return
	}
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return
	}
	q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
}

var _ source.TypedSource[reconcile.Request] = &informerSourceImpl{}
