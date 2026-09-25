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

package serviceexport

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	dynamicclient "k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/claimedresources"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/claimedresourcesnamespaces"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/isolation"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/multinsinformer"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/spec"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/cluster/serviceexport/status"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/contextstore"
	"github.com/kube-bind/kube-bind/pkg/konnector/controllers/dynamic"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	conditionsapi "github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/util/conditions"
	bindclient "github.com/kube-bind/kube-bind/sdk/client/clientset/versioned"
	bindlisters "github.com/kube-bind/kube-bind/sdk/client/listers/kubebind/v1alpha2"
)

type reconciler struct {
	// consumerSecretRefKey is the namespace/name value of the APIServiceBinding kubeconfig secret reference.
	consumerSecretRefKey     string
	providerNamespace        string
	serviceNamespaceInformer dynamic.Informer[bindlisters.APIServiceNamespaceLister]

	consumerConfig, providerConfig *rest.Config

	syncStore contextstore.Store // by APIServiceExport name. This includes same ctx for resourc claims and crds.

	getCRD               func(name string) (*apiextensionsv1.CustomResourceDefinition, error)
	getServiceBinding    func(name string) (*kubebindv1alpha2.APIServiceBinding, error)
	getRemoteBoundSchema func(ctx context.Context, name string) (*kubebindv1alpha2.BoundSchema, error)

	updateRemoteBoundSchema func(ctx context.Context, boundSchema *kubebindv1alpha2.BoundSchema) error
}

func (r *reconciler) reconcile(ctx context.Context, namespace, name string, export *kubebindv1alpha2.APIServiceExport) error {
	errs := []error{}

	if err := r.ensureControllers(ctx, namespace, name, export); err != nil {
		errs = append(errs, err)
	}

	if export != nil {
		if err := r.ensureServiceBindingConditionCopied(ctx, export); err != nil {
			errs = append(errs, err)
		}
		if err := r.ensureCRDConditionsCopiedToBoundSchema(ctx, export); err != nil {
			errs = append(errs, err)
		}
	}

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureControllers(ctx context.Context, namespace, name string, export *kubebindv1alpha2.APIServiceExport) error {
	logger := klog.FromContext(ctx)
	exportKey := contextstore.Key(namespace + "." + name) // Key for the export

	if export == nil {
		// Clean up any controllers associated with this export
		deleted := r.syncStore.BulkDeletePrefixed(exportKey)
		for _, c := range deleted {
			logger.V(1).Info("Stopping APIServiceExport sync", "key", c.Key(), "reason", "NoAPIServiceExport")
		}

		return nil
	}

	// any binding that references this resource?
	binding, err := r.getServiceBinding(export.Name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if binding == nil {
		// Stop all controllers for this export
		deleted := r.syncStore.BulkDeletePrefixed(exportKey)
		for _, k := range deleted {
			logger.V(1).Info("Stopping APIServiceExport sync", "key", k.Key(), "reason", "NoAPIServiceBinding")
		}
		return nil
	}

	// Process each resource referenced by the export
	var errs []error
	processedSchemas := make(map[string]bool)
	var isClusterScoped bool

	for _, res := range export.Spec.Resources {
		name := res.ResourceGroupName()
		// Fetch the BoundSchema
		schema, err := r.getRemoteBoundSchema(ctx, name)
		if err != nil {
			if errors.IsNotFound(err) {
				// Stop the controller for this schema if it does not exists.
				key := contextstore.NewKey(namespace, name, name)
				deleted := r.syncStore.BulkDeletePrefixed(key)
				for _, k := range deleted {
					logger.V(1).Info("Stopping APIServiceExport sync", "key", k.Key(), "reason", "BoundSchema not found")
				}
				continue
			}
			errs = append(errs, err)
			continue
		}

		// Start/update controller for this schema
		if err := r.ensureControllerForSchema(ctx, export, binding, schema); err != nil {
			errs = append(errs, err)
		}

		processedSchemas[name] = true // This is only schemas names (suffix)
		isClusterScoped = schema.Spec.InformerScope == kubebindv1alpha2.ClusterScope
	}

	// Ensure controller for permission claims
	if err := r.ensureControllersForPermissionClaims(ctx, export, binding, isClusterScoped); err != nil {
		errs = append(errs, err)
	}

	// Stop controllers for schemas that are no longer referenced.
	// This will be `exportNamespace.exportName.<schemaName>`
	contexts := r.syncStore.ListPrefixed(exportKey)
	for _, c := range contexts {
		schemaName := strings.TrimPrefix(string(c.Key()), string(exportKey)+".") // schemaName will be processed schema names.
		// Skip permission claim controllers - they are handled separately in ensureControllersForPermissionClaims
		if strings.HasPrefix(schemaName, "claim.") {
			continue
		}
		if !processedSchemas[schemaName] {
			logger.V(1).Info("Stopping APIServiceExport resource sync", "key", c.Key(), "reason", "Schema no longer referenced")
			r.syncStore.Delete(c.Key())
		}
	}

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureControllerForSchema(ctx context.Context, export *kubebindv1alpha2.APIServiceExport, binding *kubebindv1alpha2.APIServiceBinding, schema *kubebindv1alpha2.BoundSchema) error {
	logger := klog.FromContext(ctx)
	key := contextstore.NewKey(export.Namespace, export.Name, schema.Name)

	c, found := r.syncStore.Get(key)
	if found {
		if c.Generation == export.Generation {
			return nil // all as expected
		}

		logger.V(1).Info("Stopping APIServiceExport resource sync", "key", key, "reason", "GenerationChanged", "generation", schema.Generation)
		r.syncStore.Delete(key)
	}

	// At this point we dont have a controller for this schema.

	// start a new syncer
	var syncVersion string
	for _, v := range schema.Spec.Versions {
		if v.Served {
			syncVersion = v.Name
			break
		}
	}
	if syncVersion == "" {
		return fmt.Errorf("no served version found for BoundSchema %s", schema.Name)
	}

	gvr := runtimeschema.GroupVersionResource{
		Group:    schema.Spec.Group,
		Version:  syncVersion,
		Resource: schema.Spec.Names.Plural,
	}

	dynamicConsumerClient := dynamicclient.NewForConfigOrDie(r.consumerConfig)
	dynamicProviderClient := dynamicclient.NewForConfigOrDie(r.providerConfig)

	providerNamespaceUID := ""
	if pns, err := dynamicProviderClient.Resource(runtimeschema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}).Get(ctx, r.providerNamespace, metav1.GetOptions{}); err != nil {
		return err
	} else {
		providerNamespaceUID = string(pns.GetUID())
	}

	consumerInf := dynamicinformer.NewDynamicSharedInformerFactory(dynamicConsumerClient, time.Minute*30)

	var providerInf multinsinformer.GetterInformer

	if schema.Spec.Scope == apiextensionsv1.ClusterScoped || schema.Spec.InformerScope == kubebindv1alpha2.ClusterScope {
		factory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicProviderClient, time.Minute*30)
		factory.ForResource(gvr).Lister() // wire the GVR up in the informer factory
		providerInf = multinsinformer.GetterInformerWrapper{
			GVR:      gvr,
			Delegate: factory,
		}
	} else {
		var err error
		providerInf, err = multinsinformer.NewDynamicMultiNamespaceInformer(
			gvr,
			r.providerNamespace,
			r.providerConfig,
			r.serviceNamespaceInformer,
		)
		if err != nil {
			return err
		}
	}

	var isolationStrategy isolation.Strategy
	switch {
	case schema.Spec.Scope == apiextensionsv1.NamespaceScoped:
		providerBindClient, err := bindclient.NewForConfig(r.providerConfig)
		if err != nil {
			return err
		}

		isolationStrategy = isolation.NewServiceNamespaced(
			r.providerNamespace,
			r.serviceNamespaceInformer,
			func(ctx context.Context, name string) (*kubebindv1alpha2.APIServiceNamespace, error) {
				sn := &kubebindv1alpha2.APIServiceNamespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: r.providerNamespace,
						OwnerReferences: []metav1.OwnerReference{
							*metav1.NewControllerRef(export, kubebindv1alpha2.SchemeGroupVersion.WithKind("APIServiceExport")),
						},
					},
				}

				return providerBindClient.KubeBindV1alpha2().APIServiceNamespaces(sn.Namespace).Create(ctx, sn, metav1.CreateOptions{})
			})

	case export.Spec.Isolation == kubebindv1alpha2.IsolationNone:
		logger.V(4).Info("Using None isolation strategy", "export", export.Name)
		isolationStrategy = isolation.NewNone(r.providerNamespace, providerNamespaceUID)

	case export.Spec.Isolation == kubebindv1alpha2.IsolationPrefixed:
		logger.V(4).Info("Using Prefixed isolation strategy", "export", export.Name)
		isolationStrategy = isolation.NewPrefixed(r.providerNamespace, providerNamespaceUID)

	case export.Spec.Isolation == kubebindv1alpha2.IsolationNamespaced:
		logger.V(4).Info("Using Namespaced isolation strategy", "export", export.Name)
		isolationStrategy = isolation.NewNamespaced(r.providerNamespace)
	default:
		// Default to None isolation strategy if no valid isolation strategy is specified
		// This should never happen due to validation, but we add this as a safety net.
		logger.V(2).Info("Using default None isolation strategy", "export", export.Name)
		isolationStrategy = isolation.NewNone(r.providerNamespace, providerNamespaceUID)
	}

	specCtrl, err := spec.NewController(
		isolationStrategy,
		export, // pass the export to establish owner references on ServiceNamespace creation
		gvr,
		r.providerNamespace,
		providerNamespaceUID,
		r.consumerConfig,
		r.providerConfig,
		consumerInf.ForResource(gvr),
		providerInf,
		r.serviceNamespaceInformer,
		binding.Spec.ProviderID,
		binding.Spec.IsDefault,
	)
	if err != nil {
		runtime.HandleError(err)
		return nil // nothing we can do here
	}

	statusCtrl, err := status.NewController(
		isolationStrategy,
		gvr,
		r.providerNamespace,
		providerNamespaceUID,
		r.consumerConfig,
		r.providerConfig,
		consumerInf.ForResource(gvr),
		providerInf,
		r.serviceNamespaceInformer,
		binding.Spec.ProviderID,
		binding.Spec.IsDefault,
	)
	if err != nil {
		runtime.HandleError(err)
		return nil // nothing we can do here
	}

	// Create a new context for this controller
	ctxWithCancel, cancel := context.WithCancel(ctx) //nolint:gosec

	consumerInf.Start(ctxWithCancel.Done())
	providerInf.Start(ctxWithCancel)

	go func() {
		// to not block the main thread
		consumerSynced := consumerInf.WaitForCacheSync(ctxWithCancel.Done())
		logger.V(2).Info("Synced informers", "key", key, "consumer", slices.Collect(maps.Keys(consumerSynced)))

		providerSynced := providerInf.WaitForCacheSync(ctxWithCancel.Done())
		logger.V(2).Info("Synced informers", "key", key, "provider", slices.Collect(maps.Keys(providerSynced)))

		go specCtrl.Start(ctxWithCancel, 1)
		go statusCtrl.Start(ctxWithCancel, 1)
	}()

	r.syncStore.Set(key, contextstore.SyncContext{
		Generation: export.Generation,
		Cancel:     cancel,
	})

	return nil
}

func (r *reconciler) ensureControllersForPermissionClaims(
	ctx context.Context,
	export *kubebindv1alpha2.APIServiceExport,
	binding *kubebindv1alpha2.APIServiceBinding,
	isClusterScoped bool, // schema.Spec.InformerScope == kubebindv1alpha2.ClusterScope
) error {
	logger := klog.FromContext(ctx)

	// Track processed claims for cleanup
	processedClaims := make(map[string]bool)
	var errs []error

	// Process each permission claim
	for _, claim := range binding.Status.PermissionClaims {
		claimGVR, err := kubebindv1alpha2.ResolveClaimableAPI(claim)
		if err != nil {
			logger.Info("skipping unsupported claim", "claim", claim, "error", err)
			continue
		}

		// Create unique key for this claim controller
		claimKey := contextstore.NewKey(export.Namespace, export.Name, "claim", claimGVR.String())
		processedClaims[claimKey.String()] = true

		// Check if controller already exists with correct generation
		c, found := r.syncStore.Get(claimKey)
		if found {
			if c.Generation == binding.Generation {
				continue // controller is up to date
			}
			// Generation changed, stop old controller
			logger.V(1).Info("Stopping permission claim controller", "key", claimKey, "reason", "GenerationChanged")
			r.syncStore.Delete(claimKey)
		}

		// Start new controller for this claim
		if err := r.ensureControllerForPermissionClaim(ctx, binding, claim, export, claimGVR, isClusterScoped, claimKey); err != nil {
			errs = append(errs, err)
		}
	}

	// Cleanup controllers for claims that are no longer present
	claimPrefix := contextstore.NewKey(export.Namespace, export.Name, "claim")
	contexts := r.syncStore.ListPrefixed(claimPrefix)
	for _, c := range contexts {
		if !processedClaims[c.Key().String()] {
			logger.V(1).Info("Stopping permission claim controller", "key", c.Key(), "reason", "Claim no longer present")
			r.syncStore.Delete(c.Key())
		}
	}

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureControllerForPermissionClaim(
	ctx context.Context,
	binding *kubebindv1alpha2.APIServiceBinding,
	claim kubebindv1alpha2.PermissionClaim,
	export *kubebindv1alpha2.APIServiceExport,
	claimGVR runtimeschema.GroupVersionResource,
	isClusterScoped bool,
	claimKey contextstore.Key,
) error {
	logger := klog.FromContext(ctx)

	dynamicProviderClient := dynamicclient.NewForConfigOrDie(r.providerConfig)
	dynamicConsumerClient := dynamicclient.NewForConfigOrDie(r.consumerConfig)

	// Create consumer informer factory. This is always unfiltered, as we might be getting objcts from reference,
	// label or named. We need to see all objects to determine if they are claimed.
	defaultConsumerInf := dynamicinformer.NewDynamicSharedInformerFactory(dynamicConsumerClient, time.Minute*30)

	var defaultProviderInf multinsinformer.GetterInformer // will hold the default provider informer either for cluster or namespace scoped.
	if isClusterScoped {
		factory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicProviderClient, time.Minute*30)
		factory.ForResource(claimGVR).Lister() // wire the GVR up in the informer factory
		defaultProviderInf = multinsinformer.GetterInformerWrapper{
			GVR:      claimGVR,
			Delegate: factory,
		}
	} else {
		var err error
		defaultProviderInf, err = multinsinformer.NewDynamicMultiNamespaceInformer(
			claimGVR,
			r.providerNamespace,
			r.providerConfig,
			r.serviceNamespaceInformer,
		)

		if err != nil {
			logger.Info("aborting", "error", err)
			return err
		}
	}

	serviceNamespaceChannel := make(chan string, 100)

	claimedCtrl, err := claimedresources.NewController(
		claimGVR,
		claim,
		export,
		r.providerNamespace,
		r.consumerConfig,
		r.providerConfig,
		defaultConsumerInf.ForResource(claimGVR),
		defaultProviderInf,
		r.serviceNamespaceInformer,
		serviceNamespaceChannel,
	)
	if err != nil {
		return err
	}

	claimedNamespacesCtrl, err := claimedresourcesnamespaces.NewController(
		claimGVR,
		claim,
		export,
		r.providerNamespace,
		r.providerConfig,
		r.consumerConfig,
		defaultConsumerInf.ForResource(claimGVR),
		r.serviceNamespaceInformer,
		serviceNamespaceChannel,
	)
	if err != nil {
		return err
	}

	logger.Info("creating claim reconciler", "gvr", claimGVR, "key", claimKey)

	ctxWithCancel, cancel := context.WithCancel(ctx) //nolint:gosec
	// Start the informers and controllers in a goroutine
	go func() {
		defer r.syncStore.Delete(claimKey)
		defaultConsumerInf.Start(ctxWithCancel.Done())

		// Wait for consumer informers to sync
		consumerSynced := defaultConsumerInf.WaitForCacheSync(ctxWithCancel.Done())
		logger.V(2).Info("Synced consumer informers", "consumer", slices.Collect(maps.Keys(consumerSynced)), "key", claimKey)

		// IMPORTANT: WE need to start namespaces controller before the claimed resources controller,
		// as the later depends on the former to ensure service namespaces are present.
		// Else provider informers will never sync and the controller will not start.
		// check: claimedresourcesnamespaces/README.md for more details.
		go claimedNamespacesCtrl.Start(ctxWithCancel, 1)

		// Start provider informer and wait for sync
		defaultProviderInf.Start(ctxWithCancel)
		providerSynced := defaultProviderInf.WaitForCacheSync(ctxWithCancel.Done())
		logger.V(2).Info("Synced provider informers", "provider", slices.Collect(maps.Keys(providerSynced)), "key", claimKey)

		// Start the claimed resources controller
		claimedCtrl.Start(ctxWithCancel, 1)
	}()

	// Store the controller context for tracking
	r.syncStore.Set(claimKey, contextstore.SyncContext{
		Generation: binding.Generation,
		Cancel:     cancel,
	})

	return nil
}

func (r *reconciler) ensureCRDConditionsCopiedToBoundSchema(ctx context.Context, export *kubebindv1alpha2.APIServiceExport) error {
	if export == nil {
		return nil
	}
	var errs []error
	allValid := true // assume all BoundAPIResourceSchemas are valid
	for _, res := range export.Spec.Resources {
		boundSchema, err := r.getRemoteBoundSchema(ctx, res.ResourceGroupName())
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			errs = append(errs, err)
			continue
		}

		crd, err := r.getCRD(boundSchema.Name)
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			errs = append(errs, err)
			continue
		}

		boundSchemaIndex := map[conditionsapi.ConditionType]int{}
		for i, c := range boundSchema.Status.Conditions {
			boundSchemaIndex[c.Type] = i
		}
		for _, c := range crd.Status.Conditions {
			if conditionsapi.ConditionType(c.Type) == conditionsapi.ReadyCondition {
				continue
			}

			severity := conditionsapi.ConditionSeverityError
			if c.Status == apiextensionsv1.ConditionTrue {
				severity = conditionsapi.ConditionSeverityNone
			}
			copied := conditionsapi.Condition{
				Type:               conditionsapi.ConditionType(c.Type),
				Status:             corev1.ConditionStatus(c.Status),
				Severity:           severity, // CRD conditions have no severity
				LastTransitionTime: c.LastTransitionTime,
				Reason:             c.Reason,
				Message:            c.Message,
			}

			// update or append
			if i, found := boundSchemaIndex[conditionsapi.ConditionType(c.Type)]; found {
				boundSchema.Status.Conditions[i] = copied
			} else {
				boundSchema.Status.Conditions = append(boundSchema.Status.Conditions, copied)
			}
		}
		conditions.SetSummary(boundSchema)

		boundSchema.Status.AcceptedNames = crd.Status.AcceptedNames
		boundSchema.Status.StoredVersions = crd.Status.StoredVersions

		if err := r.updateRemoteBoundSchema(ctx, boundSchema); err != nil {
			errs = append(errs, err)
			allValid = false // at least one BoundSchemas is not valid
		}
	}

	// Set APIServiceExport Ready condition based on all BoundSchemas
	if allValid {
		conditions.MarkTrue(
			export,
			kubebindv1alpha2.APIServiceExportConditionConnected,
		)
	} else {
		conditions.MarkFalse(
			export,
			kubebindv1alpha2.APIServiceExportConditionConnected,
			"BoundSchemasNotValid",
			conditionsapi.ConditionSeverityWarning,
			"One or more BoundSchemas are not valid",
		)
	}

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureServiceBindingConditionCopied(_ context.Context, export *kubebindv1alpha2.APIServiceExport) error {
	binding, err := r.getServiceBinding(export.Name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	} else if errors.IsNotFound(err) {
		conditions.MarkFalse(
			export,
			kubebindv1alpha2.APIServiceExportConditionConnected,
			"APIServiceBindingNotFound",
			conditionsapi.ConditionSeverityInfo,
			"No APIServiceBinding exists.",
		)

		conditions.MarkFalse(
			export,
			kubebindv1alpha2.APIServiceExportConditionConsumerInSync,
			"NA",
			conditionsapi.ConditionSeverityInfo,
			"No APIServiceBinding exists.",
		)

		return nil
	}

	conditions.MarkTrue(export, kubebindv1alpha2.APIServiceExportConditionConnected)

	if inSync := conditions.Get(binding, kubebindv1alpha2.APIServiceBindingConditionSchemaInSync); inSync != nil {
		inSync := inSync.DeepCopy()
		inSync.Type = kubebindv1alpha2.APIServiceExportConditionConsumerInSync
		conditions.Set(export, inSync)
	} else {
		conditions.MarkFalse(
			export,
			kubebindv1alpha2.APIServiceExportConditionConsumerInSync,
			"Unknown",
			conditionsapi.ConditionSeverityInfo,
			"APIServiceBinding %s in the consumer cluster does not have a SchemaInSync condition.",
			binding.Name,
		)
	}

	return nil
}
