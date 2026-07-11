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

package serviceexportrequest

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kube-bind/kube-bind/backend/kubernetes/resources"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	"github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2/helpers"
	conditionsapi "github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/util/conditions"
)

type reconciler struct {
	informerScope      kubebindv1alpha2.InformerScope
	isolation          kubebindv1alpha2.Isolation
	schemaSource       string
	schemaSyncInterval time.Duration

	getBoundSchema    func(ctx context.Context, cl client.Client, namespace, name string) (*kubebindv1alpha2.BoundSchema, error)
	createBoundSchema func(ctx context.Context, cl client.Client, schema *kubebindv1alpha2.BoundSchema) error
	updateBoundSchema func(ctx context.Context, cl client.Client, schema *kubebindv1alpha2.BoundSchema) error

	getServiceExport           func(ctx context.Context, cache cache.Cache, ns, name string) (*kubebindv1alpha2.APIServiceExport, error)
	createServiceExport        func(ctx context.Context, cl client.Client, resource *kubebindv1alpha2.APIServiceExport) error
	deleteServiceExportRequest func(ctx context.Context, cl client.Client, namespace, name string) error
}

func (r *reconciler) reconcile(ctx context.Context, cl client.Client, cache cache.Cache, req *kubebindv1alpha2.APIServiceExportRequest) error {
	export, err := r.getServiceExport(ctx, cache, req.Namespace, req.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get APIServiceExport: %w", err)
	}
	// We must ensure schemas are created in form of boundSchemas first for the validation.
	// Worst case scenario if validation fails, we will reuse schemas for same consumer once issues are fixed.
	if err := r.ensureBoundSchemas(ctx, cl, export, req); err != nil {
		conditions.SetSummary(req)
		return fmt.Errorf("failed to ensure bound schemas: %w", err)
	}

	if err := r.validate(ctx, cl, req); err != nil {
		conditions.SetSummary(req)
		return fmt.Errorf("failed to validate APIServiceExportRequest: %w", err)
	}

	if err := r.ensureExports(ctx, cl, export, req); err != nil {
		conditions.SetSummary(req)
		return fmt.Errorf("failed to ensure exports: %w", err)
	}

	if err := r.ensureAPIServiceNamespaces(ctx, cl, cache, req); err != nil {
		conditions.SetSummary(req)
		return fmt.Errorf("failed to ensure APIServiceNamespaces: %w", err)
	}

	conditions.SetSummary(req)

	return nil
}

// getExportedSchemas will list all schemas, exported by current backend.
// Important: getExportedSchemas is using client.Client to list resources, not cache.
// This is due to fact we use dynamic client and unstructured.Unstructured to get schemas and it
// does not quite work with dynamic cache informers:
// failed to get informer for *unstructured.UnstructuredList apis.kcp.io/v1alpha1, Kind=APIResourceSchemaList: failed to find newly started informer for apis.kcp.io/v1alpha1, Kind=APIResourceSchema"}.
func (r *reconciler) getExportedSchemas(ctx context.Context, cl client.Client) (kubebindv1alpha2.ExportedSchemas, error) {
	parts := strings.SplitN(r.schemaSource, ".", 3)
	if len(parts) != 3 { // We check this in validation, but just in case.
		return nil, fmt.Errorf("malformed schema source: %q", r.schemaSource)
	}

	gvk := schema.GroupVersionKind{
		Kind:    parts[0],
		Version: parts[1],
		Group:   parts[2],
	}

	// Ensure we have the List kind
	listGVK := gvk
	if !strings.HasSuffix(listGVK.Kind, "List") {
		listGVK.Kind += "List"
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listGVK)

	// TODO(mjudeikis): This is hardcoded here and in handlers.go for now.
	labelSelector := labels.Set{
		resources.ExportedCRDsLabel: "true",
	}

	listOpts := make([]client.ListOption, 0, 1)
	listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: labelSelector.AsSelector()})

	if err := cl.List(ctx, list, listOpts...); err != nil {
		return nil, err
	}

	boundSchemas := make(kubebindv1alpha2.ExportedSchemas, len(list.Items))
	for _, item := range list.Items {
		boundSchema, err := helpers.UnstructuredToBoundSchema(item)
		if err != nil {
			return nil, err
		}
		boundSchemas[boundSchema.ResourceGroupName()] = boundSchema
	}

	return boundSchemas, nil
}

func (r *reconciler) ensureBoundSchemas(ctx context.Context, cl client.Client, export *kubebindv1alpha2.APIServiceExport, req *kubebindv1alpha2.APIServiceExportRequest) error {
	exportedSchemas, err := r.getExportedSchemas(ctx, cl)
	if err != nil {
		return err
	}

	for _, res := range req.Spec.Resources {
		if len(res.Versions) == 0 {
			continue
		}

		for _, boundSchema := range exportedSchemas {
			if boundSchema.Spec.Group == res.Group && boundSchema.Spec.Names.Plural == res.Resource {
				boundSchema.Name = res.ResourceGroupName()
				boundSchema.Namespace = req.Namespace
				boundSchema.Spec.InformerScope = r.informerScope
				boundSchema.ResourceVersion = ""

				if err := r.createOrUpdateBoundSchema(ctx, cl, export, boundSchema, req.Spec.SchemaUpdatePolicy); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (r *reconciler) createOrUpdateBoundSchema(ctx context.Context, cl client.Client, export *kubebindv1alpha2.APIServiceExport, desired *kubebindv1alpha2.BoundSchema, schemaUpdatePolicy kubebindv1alpha2.SchemaUpdatePolicy) error {
	logger := klog.FromContext(ctx)

	// If namespaced isolation is configured for cluster-scoped objects,
	// we need to rewrite the BoundSchema's scope accordingly.
	if desired.Spec.Scope == apiextensionsv1.NamespaceScoped && r.isolation == kubebindv1alpha2.IsolationNamespaced {
		desired.Spec.Scope = apiextensionsv1.ClusterScoped
	}

	hash, err := helpers.BoundSchemaSpecHash(&desired.Spec)
	if err != nil {
		return err
	}

	existing, err := r.getBoundSchema(ctx, cl, desired.Namespace, desired.Name)
	if err != nil && !apierrors.IsNotFound(err) && !strings.Contains(err.Error(), "no matches for kind") {
		return err
	}

	if existing != nil {
		var needsUpdate bool
		// When export is nil (APIServiceExport not yet created), we skip owner-reference
		// management but still proceed with schema sync so BoundSchemas are ready
		// before the export is created on the next reconcile pass.
		// For policy=Never this is a no-op; for policy=Always
		// it allows hash-based spec sync to run independently of export lifecycle.
		if export != nil {
			if !metav1.IsControlledBy(existing, export) {
				if err := controllerutil.SetControllerReference(export, existing, cl.Scheme()); err != nil {
					return fmt.Errorf("failed to set owner reference on BoundSchema %s: %w", desired.Name, err)
				}
				needsUpdate = true
			}
		}

		if schemaUpdatePolicy == kubebindv1alpha2.SchemaUpdatePolicyAlways {
			if existing.Annotations[kubebindv1alpha2.SourceSpecHashAnnotationKey] != hash {
				existing.Spec = desired.Spec
				if existing.Annotations == nil {
					existing.Annotations = make(map[string]string)
				}
				existing.Annotations[kubebindv1alpha2.SourceSpecHashAnnotationKey] = hash
				needsUpdate = true
			}
		}

		if needsUpdate {
			if err := r.updateBoundSchema(ctx, cl, existing); err != nil {
				return fmt.Errorf("failed to update BoundSchema %s: %w", desired.Name, err)
			}
			logger.V(6).Info("Updated existing BoundSchema",
				"boundSchema", desired.Name,
				"namespace", desired.Namespace)
		}
		return nil
	}

	// Create path: new BoundSchema.
	if desired.Annotations == nil {
		desired.Annotations = make(map[string]string)
	}
	desired.Annotations[kubebindv1alpha2.SourceSpecHashAnnotationKey] = hash

	return r.createBoundSchema(ctx, cl, desired)
}

func (r *reconciler) ensureExports(ctx context.Context, cl client.Client, existingExport *kubebindv1alpha2.APIServiceExport, req *kubebindv1alpha2.APIServiceExportRequest) error {
	logger := klog.FromContext(ctx)

	var schemas []*kubebindv1alpha2.BoundSchema
	if req.Status.Phase == kubebindv1alpha2.APIServiceExportRequestPhasePending {
		for _, res := range req.Spec.Resources {
			name := res.ResourceGroupName()
			boundSchema, err := r.getBoundSchema(ctx, cl, req.Namespace, name)
			if err != nil {
				if apierrors.IsNotFound(err) {
					conditions.MarkFalse(
						req,
						kubebindv1alpha2.APIServiceExportRequestConditionExportsReady,
						"BoundSchemaNotFound",
						conditionsapi.ConditionSeverityError,
						"BoundSchema %s in the service provider cluster not found",
						name,
					)
					return err
				}
				return err
			}

			// Collect all schemas for hashing.
			// TODO(mjudeikis) Scope is same for all crds so we keep stamping it over. We might want to change this
			schemas = append(schemas, boundSchema)
		}

		if existingExport != nil {
			conditions.MarkTrue(req, kubebindv1alpha2.APIServiceExportRequestConditionExportsReady)
			return nil
		}

		hash, err := helpers.BoundSchemasSpecHash(schemas)
		if err != nil {
			return err
		}
		export := &kubebindv1alpha2.APIServiceExport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name,
				Namespace: req.Namespace,
				Annotations: map[string]string{
					kubebindv1alpha2.SourceSpecHashAnnotationKey: hash,
				},
			},
			Spec: kubebindv1alpha2.APIServiceExportSpec{
				InformerScope: r.informerScope,
				Isolation:     r.isolation,
			},
		}

		for _, res := range req.Spec.Resources {
			export.Spec.Resources = append(export.Spec.Resources, kubebindv1alpha2.APIServiceExportResource{
				GroupResource: kubebindv1alpha2.GroupResource{
					Group:    res.Group,
					Resource: res.Resource,
				},
				Versions: res.Versions,
			})
		}
		export.Spec.PermissionClaims = req.Spec.PermissionClaims

		logger.V(1).Info("Creating APIServiceExport", "name", export.Name, "namespace", export.Namespace)
		if err := r.createServiceExport(ctx, cl, export); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		}

		conditions.MarkTrue(req, kubebindv1alpha2.APIServiceExportRequestConditionExportsReady)
		req.Status.Phase = kubebindv1alpha2.APIServiceExportRequestPhaseSucceeded

		if time.Since(req.CreationTimestamp.Time) > time.Minute {
			req.Status.Phase = kubebindv1alpha2.APIServiceExportRequestPhaseFailed
			req.Status.TerminalMessage = conditions.GetMessage(req, kubebindv1alpha2.APIServiceExportRequestConditionExportsReady)
		}
	}

	if time.Since(req.CreationTimestamp.Time) > 10*time.Minute {
		logger.Info("Deleting service binding request %s/%s", req.Namespace, req.Name, "reason", "timeout", "age", time.Since(req.CreationTimestamp.Time))
		return r.deleteServiceExportRequest(ctx, cl, req.Namespace, req.Name)
	}

	return nil
}

// Validate validates if the APIServiceExportRequest is in a valid state.
// Currently it validates if all requested schemas are of the same scope.
func (r *reconciler) validate(ctx context.Context, cl client.Client, req *kubebindv1alpha2.APIServiceExportRequest) error {
	exportedSchemas, err := r.getExportedSchemas(ctx, cl)
	if err != nil {
		return err
	}

	if len(exportedSchemas) == 0 {
		conditions.MarkFalse(
			req,
			kubebindv1alpha2.APIServiceExportRequestConditionExportsReady,
			"SchemaNotFound",
			conditionsapi.ConditionSeverityError,
			"Schema not found",
		)
		return fmt.Errorf("no exported schemas found")
	}

	first := apiextensionsv1.ResourceScope("")
	for _, res := range req.Spec.Resources {
		boundSchema, ok := exportedSchemas[res.ResourceGroupName()]
		if !ok {
			conditions.MarkFalse(
				req,
				kubebindv1alpha2.APIServiceExportRequestConditionExportsReady,
				"SchemaNotFound",
				conditionsapi.ConditionSeverityError,
				"Schema %s not found",
				res.ResourceGroupName(),
			)
			return fmt.Errorf("schema %s not found", res.ResourceGroupName())
		}
		if first == apiextensionsv1.ResourceScope("") {
			first = boundSchema.Spec.Scope
			continue
		}
		if boundSchema.Spec.Scope != first {
			conditions.MarkFalse(req,
				kubebindv1alpha2.APIServiceExportRequestConditionExportsReady,
				"DifferentScopes",
				conditionsapi.ConditionSeverityError,
				"Different scopes found: %v",
				boundSchema.Spec.Scope,
			)
			return fmt.Errorf("different scopes found for claimed resources: %v", boundSchema.Name)
		}
	}

	return nil
}

func (r *reconciler) ensureAPIServiceNamespaces(ctx context.Context, cl client.Client, cache cache.Cache, req *kubebindv1alpha2.APIServiceExportRequest) error {
	logger := klog.FromContext(ctx)

	// TODO(mjudeikis): We have this object above already, pass it down to avoid extra get.
	export := &kubebindv1alpha2.APIServiceExport{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(req), export); err != nil {
		return fmt.Errorf("failed to get APIServiceExport %s/%s: %w", req.Namespace, req.Name, err)
	}

	for _, ns := range req.Spec.Namespaces {
		apiServiceNamespace := helpers.APIServiceNamespaceFromExport(export, ns.Name)
		currentAPIServiceNamespace := &kubebindv1alpha2.APIServiceNamespace{}
		err := cache.Get(ctx, client.ObjectKeyFromObject(apiServiceNamespace), currentAPIServiceNamespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("Creating APIServiceNamespace", "name", apiServiceNamespace.Name, "namespace", apiServiceNamespace.Namespace)
				if err := cl.Create(ctx, apiServiceNamespace); err != nil {
					if apierrors.IsAlreadyExists(err) {
						continue
					}
					return err
				}
			} else {
				return err
			}
		}
	}

	return nil
}
