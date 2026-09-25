/*
Copyright 2022 The Kube Bind Authors.

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

package status

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	dynamicclient "k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/isolation"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/multinsinformer"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/dynamic"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	bindlisters "github.com/kube-bind/kube-bind/sdk/client/listers/kubebind/v1alpha2"
)

const (
	controllerName = "kube-bind-konnector-cluster-status"
)

// NewController returns a new controller reconciling status of upstream to downstream.
func NewController(
	isolationStrategy isolation.Strategy,
	gvr schema.GroupVersionResource,
	providerNamespace string,
	providerNamespaceUID string,
	consumerConfig, providerConfig *rest.Config,
	consumerDynamicInformer informers.GenericInformer,
	providerDynamicInformer multinsinformer.GetterInformer,
	serviceNamespaceInformer dynamic.Informer[bindlisters.APIServiceNamespaceLister],
	providerID string,
	isDefault bool,
) (*controller, error) {
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerName})

	logger := klog.Background().WithValues("controller", controllerName)

	consumerConfig = rest.CopyConfig(consumerConfig)
	consumerConfig = rest.AddUserAgent(consumerConfig, controllerName)

	providerConfig = rest.CopyConfig(providerConfig)
	providerConfig = rest.AddUserAgent(providerConfig, controllerName)

	consumerClient, err := dynamicclient.NewForConfig(consumerConfig)
	if err != nil {
		return nil, err
	}
	providerClient, err := dynamicclient.NewForConfig(providerConfig)
	if err != nil {
		return nil, err
	}

	dynamicConsumerLister := dynamiclister.New(consumerDynamicInformer.Informer().GetIndexer(), gvr)
	c := &controller{
		queue: queue,

		gvr:               gvr,
		providerNamespace: providerNamespace,

		consumerClient: consumerClient,
		providerClient: providerClient,

		consumerDynamicLister:  dynamicConsumerLister,
		consumerDynamicIndexer: consumerDynamicInformer.Informer().GetIndexer(),

		providerDynamicInformer: providerDynamicInformer,

		serviceNamespaceInformer: serviceNamespaceInformer,

		reconciler: reconciler{
			isolationStrategy: isolationStrategy,

			getConsumerObject: func(ns, name string) (obj *unstructured.Unstructured, err error) {
				if ns != "" {
					obj, err = dynamicConsumerLister.Namespace(ns).Get(name)
				} else {
					obj, err = dynamicConsumerLister.Get(name)
				}

				if err != nil {
					return nil, err
				}

				return obj.DeepCopy(), nil
			},
			updateConsumerObject: func(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				return consumerClient.Resource(gvr).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{})
			},
			updateConsumerObjectStatus: func(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				return consumerClient.Resource(gvr).Namespace(obj.GetNamespace()).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
			},
			deleteProviderObject: func(ctx context.Context, ns, name string) error {
				return providerClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
			},
		},
	}

	if _, err := consumerDynamicInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if !shouldProcess(obj, providerID, isDefault) {
				return
			}
			c.enqueueConsumer(logger, obj)
		},
		UpdateFunc: func(_, newObj any) {
			if !shouldProcess(newObj, providerID, isDefault) {
				return
			}
			c.enqueueConsumer(logger, newObj)
		},
		DeleteFunc: func(obj any) {
			if !shouldProcess(obj, providerID, isDefault) {
				return
			}
			c.enqueueConsumer(logger, obj)
		},
	}); err != nil {
		return nil, err
	}

	if err := providerDynamicInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			c.enqueueProvider(logger, obj)
		},
		UpdateFunc: func(_, newObj any) {
			c.enqueueProvider(logger, newObj)
		},
		DeleteFunc: func(obj any) {
			c.enqueueProvider(logger, obj)
		},
	}); err != nil {
		return nil, err
	}

	return c, nil
}

// controller reconciles status of upstream to downstream.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	gvr               schema.GroupVersionResource
	providerNamespace string

	consumerClient, providerClient dynamicclient.Interface

	consumerDynamicLister  dynamiclister.Lister
	consumerDynamicIndexer cache.Indexer

	providerDynamicInformer multinsinformer.GetterInformer

	serviceNamespaceInformer dynamic.Informer[bindlisters.APIServiceNamespaceLister]

	reconciler
}

func (c *controller) enqueueProvider(logger klog.Logger, obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	// Try to map the provider name back to the consumer name,
	// but only to check if we "own" the object; we will actually
	// enqueue the provider key after all.
	consumerKey, err := c.isolationStrategy.ToConsumerKey(types.NamespacedName{
		Namespace: ns,
		Name:      name,
	})
	if err != nil {
		if !errors.IsNotFound(err) {
			runtime.HandleError(err)
		}
		return
	}

	if consumerKey == nil {
		return
	}

	logger.V(2).Info("queueing Unstructured", "queued", key)
}

func shouldProcess(obj any, providerID string, isDefault bool) bool {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		// Try DeletedFinalStateUnknown
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return false
		}
		u, ok = tombstone.Obj.(*unstructured.Unstructured)
		if !ok {
			return false
		}
	}

	annotations := u.GetAnnotations()
	if annotations == nil {
		return isDefault
	}
	val, ok := annotations["provider.kube-bind.io/provider-id"]
	if !ok || val == "" {
		return isDefault
	}
	return val == providerID
}

func (c *controller) enqueueConsumer(logger klog.Logger, obj any) {
	downstreamKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, name, err := cache.SplitMetaNamespaceKey(downstreamKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	providerKey, err := c.isolationStrategy.ToProviderKey(types.NamespacedName{
		Namespace: ns,
		Name:      name,
	})
	if err != nil {
		if !errors.IsNotFound(err) {
			runtime.HandleError(err)
		}
		return
	}

	if providerKey == nil {
		return
	}

	logger.V(2).Info("queueing Unstructured", "queued", providerKey)
	c.queue.Add(providerKey.String())
}

func (c *controller) enqueueServiceNamespace(logger klog.Logger, obj any) {
	snKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, name, err := cache.SplitMetaNamespaceKey(snKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if ns != c.providerNamespace {
		return // not for us
	}

	strategy, ok := c.isolationStrategy.(*isolation.ServiceNamespacedStrategy)
	if !ok { // should not happen... but...
		return
	}
	nsOnProviderCluster, err := strategy.ProviderNamespace(name)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if nsOnProviderCluster == "" {
		return // not ready
	}

	objs, err := c.providerDynamicInformer.List(nsOnProviderCluster)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, obj := range objs {
		key, err := cache.MetaNamespaceKeyFunc(obj)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logger.V(2).Info("queueing Unstructured", "queued", key, "reason", "APIServiceNamespace", "ServiceNamespaceKey", snKey)
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx).WithValues("controller", controllerName)

	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	// APIServiceNamespaces are only of interest when syncing namespaced
	// objects, and since these event handlers need the appropriate isolation
	// strategy, we only start them when necessary.
	if _, ok := c.isolationStrategy.(*isolation.ServiceNamespacedStrategy); ok {
		c.serviceNamespaceInformer.Informer().AddDynamicEventHandler(ctx, controllerName, cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				c.enqueueServiceNamespace(logger, obj)
			},
			UpdateFunc: func(_, newObj any) {
				c.enqueueServiceNamespace(logger, newObj)
			},
			DeleteFunc: func(obj any) {
				c.enqueueServiceNamespace(logger, obj)
			},
		})
	}

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	defer runtime.HandleCrash()

	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	logger := klog.FromContext(ctx).WithValues("key", key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(2).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil // we cannot do anything
	}

	logger := klog.FromContext(ctx)

	obj, err := c.providerDynamicInformer.Get(ns, name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	} else if errors.IsNotFound(err) {
		logger.V(2).Info("Upstream object disappeared")

		downstream, err := c.consumerDynamicLister.Namespace(ns).Get(name)
		if err != nil && !errors.IsNotFound(err) {
			return err
		} else if err == nil {
			if _, err := c.removeDownstreamFinalizer(ctx, downstream); err != nil {
				return err
			}
		}

		return nil
	}

	return c.reconcile(ctx, obj.(*unstructured.Unstructured))
}

func (c *controller) removeDownstreamFinalizer(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	logger := klog.FromContext(ctx)

	finalizers := []string{}
	found := false
	for _, f := range obj.GetFinalizers() {
		if f == kubebindv1alpha2.DownstreamFinalizer {
			found = true
			continue
		}
		finalizers = append(finalizers, f)
	}

	if found {
		logger.V(2).Info("removing finalizer from downstream object")
		obj = obj.DeepCopy()
		obj.SetFinalizers(finalizers)
		var err error
		if obj, err = c.consumerClient.Resource(c.gvr).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{}); err != nil && !errors.IsNotFound(err) {
			return nil, err
		}
	}

	return obj, nil
}
