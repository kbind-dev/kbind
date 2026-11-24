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

package spec

import (
	"context"
	"encoding/json"
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
	"k8s.io/utils/ptr"

	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/isolation"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/multinsinformer"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/dynamic"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	bindclient "github.com/kube-bind/kube-bind/sdk/client/clientset/versioned"
	bindlisters "github.com/kube-bind/kube-bind/sdk/client/listers/kubebind/v1alpha2"
)

const (
	controllerName = "kube-bind-konnector-cluster-spec"

	applyManager = "kube-bind.io"
)

// NewController returns a new controller reconciling downstream objects to upstream.
func NewController(
	isolationStrategy isolation.Strategy,
	apiServiceExport *kubebindv1alpha2.APIServiceExport, // used to establish owner references when create happens from the consumer side.
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

	providerConfig = rest.CopyConfig(providerConfig)
	providerConfig = rest.AddUserAgent(providerConfig, controllerName)

	providerClient, err := dynamicclient.NewForConfig(providerConfig)
	if err != nil {
		return nil, err
	}
	providerBindClient, err := bindclient.NewForConfig(providerConfig)
	if err != nil {
		return nil, err
	}
	consumerClient, err := dynamicclient.NewForConfig(consumerConfig)
	if err != nil {
		return nil, err
	}

	dynamicConsumerLister := dynamiclister.New(consumerDynamicInformer.Informer().GetIndexer(), gvr)
	c := &controller{
		queue: queue,

		consumerClient:    consumerClient,
		providerClient:    providerClient,
		providerNamespace: providerNamespace,

		consumerDynamicLister:  dynamicConsumerLister,
		consumerDynamicIndexer: consumerDynamicInformer.Informer().GetIndexer(),

		providerDynamicInformer: providerDynamicInformer,

		serviceNamespaceInformer: serviceNamespaceInformer,

		reconciler: reconciler{
			isolationStrategy: isolationStrategy,
			clusterNamespace:  providerNamespace,

			getServiceNamespace: func(name string) (*kubebindv1alpha2.APIServiceNamespace, error) {
				return serviceNamespaceInformer.Lister().APIServiceNamespaces(providerNamespace).Get(name)
			},
			createServiceNamespace: func(ctx context.Context, sn *kubebindv1alpha2.APIServiceNamespace) (*kubebindv1alpha2.APIServiceNamespace, error) {
				return providerBindClient.KubeBindV1alpha2().APIServiceNamespaces(providerNamespace).Create(ctx, sn, metav1.CreateOptions{})
			},
			getProviderObject: func(ns, name string) (*unstructured.Unstructured, error) {
				got, err := providerDynamicInformer.Get(ns, name)
				if err != nil {
					return nil, err
				}
				return got.(*unstructured.Unstructured).DeepCopy(), nil
			},
			createProviderObject: func(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				return providerClient.Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
			},
			updateProviderObject: func(ctx context.Context, obj *unstructured.Unstructured) error {
				obj.SetManagedFields(nil) // server side apply does not want this

				data, err := json.Marshal(obj.Object)
				if err != nil {
					return err
				}
				_, err = providerClient.Resource(gvr).Namespace(obj.GetNamespace()).Patch(ctx,
					obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: applyManager, Force: ptr.To(true)},
				)
				return err
			},
			deleteProviderObject: func(ctx context.Context, ns, name string) error {
				return providerClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
			},
			updateConsumerObject: func(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				return consumerClient.Resource(gvr).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{})
			},
			requeue: func(obj *unstructured.Unstructured, after time.Duration) error {
				key, err := cache.MetaNamespaceKeyFunc(obj)
				if err != nil {
					return err
				}
				queue.AddAfter(key, after)
				return nil
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

// controller reconciles downstream objects to upstream.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	providerNamespace string

	consumerClient dynamicclient.Interface
	providerClient dynamicclient.Interface

	consumerDynamicLister  dynamiclister.Lister
	consumerDynamicIndexer cache.Indexer

	providerDynamicInformer multinsinformer.GetterInformer

	serviceNamespaceInformer dynamic.Informer[bindlisters.APIServiceNamespaceLister]

	reconciler
}

func (c *controller) enqueueConsumer(logger klog.Logger, obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger.V(2).Info("queueing Unstructured", "queued", key, "reason", "consumerObject")
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

func (c *controller) enqueueProvider(logger klog.Logger, obj any) {
	providerKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, name, err := cache.SplitMetaNamespaceKey(providerKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

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

	logger.V(2).Info("queueing Unstructured", "queued", consumerKey, "reason", "providerObject")
	c.queue.Add(consumerKey.String())
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

	objs, err := c.consumerDynamicIndexer.ByIndex(cache.NamespaceIndex, name)
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

	var obj *unstructured.Unstructured
	if ns == "" {
		obj, err = c.consumerDynamicLister.Get(name)
	} else {
		obj, err = c.consumerDynamicLister.Namespace(ns).Get(name)
	}
	if err != nil && !errors.IsNotFound(err) {
		return err
	} else if errors.IsNotFound(err) {
		logger.V(2).Info("Downstream object disappeared")
		return nil
	}

	return c.reconcile(ctx, obj)
}
