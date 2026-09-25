/*
Copyright 2025 The Kube Bind Authors.

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

package bindableresourcesrequest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/kube-bind/kube-bind/backend/kubernetes"
	"github.com/kube-bind/kube-bind/pkg/indexers"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
)

const (
	controllerName = "kube-bind-backend-bindableresourcesrequest"
)

// BindableResourcesRequestReconciler reconciles a BindableResourcesRequest object.
// This controller handles direct binding requests created as CRDs when the
// backend is running without the HTTP API/OIDC flow (frontend disabled mode).
//
// The controller:
// 1. Reads the kubeconfig from the referenced secret
// 2. Creates a BindingResourceResponse secret with the kubeconfig
// 3. Creates an APIServiceExportRequest based on the template
type BindableResourcesRequestReconciler struct {
	manager mcmanager.Manager
	opts    controller.TypedOptions[mcreconcile.Request]

	informerScope kubebindv1alpha2.InformerScope
	isolation     kubebindv1alpha2.Isolation
	reconciler    reconciler
}

// NewBindableResourcesRequestReconciler returns a new BindableResourcesRequestReconciler.
func NewBindableResourcesRequestReconciler(
	ctx context.Context,
	mgr mcmanager.Manager,
	opts controller.TypedOptions[mcreconcile.Request],
	scope kubebindv1alpha2.InformerScope,
	isolation kubebindv1alpha2.Isolation,
	kubeManager *kubernetes.Manager,
) (*BindableResourcesRequestReconciler, error) {
	// Set up field indexers for BindableResourcesRequests
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kubebindv1alpha2.BindableResourcesRequest{}, indexers.BindableResourcesRequestByTemplate,
		indexers.IndexBindableResourcesRequestByTemplate); err != nil {
		return nil, fmt.Errorf("failed to setup BindableResourcesRequestByTemplate indexer: %w", err)
	}

	r := &BindableResourcesRequestReconciler{
		manager:       mgr,
		opts:          opts,
		informerScope: scope,
		isolation:     isolation,
		reconciler: reconciler{
			informerScope: scope,
			isolation:     isolation,
			kubeManager:   kubeManager,
		},
	}

	return r, nil
}

//+kubebuilder:rbac:groups=kube-bind.io,resources=bindableresourcesrequests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kube-bind.io,resources=bindableresourcesrequests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kube-bind.io,resources=bindableresourcesrequests/finalizers,verbs=update
//+kubebuilder:rbac:groups=kube-bind.io,resources=apiserviceexportrequests,verbs=get;list;watch;create
//+kubebuilder:rbac:groups=kube-bind.io,resources=apiserviceexporttemplates,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update

// Reconcile is part of the main kubernetes reconciliation loop.
func (r *BindableResourcesRequestReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling BindableResourcesRequest", "cluster", req.ClusterName, "namespace", req.Namespace, "name", req.Name)

	cl, err := r.manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get client for cluster %q: %w", req.ClusterName, err)
	}

	client := cl.GetClient()

	// Fetch the BindableResourcesRequest instance
	bindableRequest := &kubebindv1alpha2.BindableResourcesRequest{}
	if err := client.Get(ctx, req.NamespacedName, bindableRequest); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("BindableResourcesRequest not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get BindableResourcesRequest: %w", err)
	}

	// Check TTL-based deletion for completed (ttl) requests
	if bindableRequest.Status.Phase == kubebindv1alpha2.BindableResourcesRequestPhaseSucceeded ||
		bindableRequest.Status.Phase == kubebindv1alpha2.BindableResourcesRequestPhaseFailed {
		return r.handleTTL(ctx, client, bindableRequest)
	}

	// Create a copy to modify
	original := bindableRequest.DeepCopy()

	// Run the reconciliation logic
	result, err := r.reconciler.reconcile(ctx, string(req.ClusterName), client, bindableRequest)
	if err != nil {
		logger.Error(err, "Failed to reconcile BindableResourcesRequest")
		if !reflect.DeepEqual(original.Status, bindableRequest.Status) {
			if updateErr := client.Status().Update(ctx, bindableRequest); updateErr != nil {
				logger.Error(updateErr, "Failed to update BindableResourcesRequest status")
				return ctrl.Result{}, fmt.Errorf("failed to update BindableResourcesRequest status: %w", updateErr)
			}
		}
		return ctrl.Result{}, err
	}

	// Update status if it has changed
	if !reflect.DeepEqual(original.Status, bindableRequest.Status) {
		if err := client.Status().Update(ctx, bindableRequest); err != nil {
			logger.Error(err, "Failed to update BindableResourcesRequest status")
			return ctrl.Result{}, fmt.Errorf("failed to update BindableResourcesRequest status: %w", err)
		}
		logger.Info("BindableResourcesRequest status updated", "namespace", bindableRequest.Namespace, "name", bindableRequest.Name)
	}

	return result, nil
}

// handleTTL checks if the request should be deleted based on TTL settings.
func (r *BindableResourcesRequestReconciler) handleTTL(ctx context.Context, cl client.Client, req *kubebindv1alpha2.BindableResourcesRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// If no TTL is set, don't delete
	if req.Spec.TTLAfterFinished == nil {
		return ctrl.Result{}, nil
	}

	// If no completion time is set, something is wrong - skip
	if req.Status.CompletionTime == nil {
		return ctrl.Result{}, nil
	}

	ttl := req.Spec.TTLAfterFinished.Duration
	expireTime := req.Status.CompletionTime.Add(ttl)
	now := time.Now()

	if now.After(expireTime) {
		logger.Info("TTL expired, deleting BindableResourcesRequest", "name", req.Name, "namespace", req.Namespace)
		if err := cl.Delete(ctx, req); err != nil {
			if errors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("failed to delete expired BindableResourcesRequest: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Requeue to check again when TTL expires
	requeueAfter := expireTime.Sub(now)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BindableResourcesRequestReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		For(&kubebindv1alpha2.BindableResourcesRequest{}).
		Owns(&corev1.Secret{}).
		WithOptions(r.opts).
		Named(controllerName).
		Complete(r)
}

type reconciler struct {
	informerScope kubebindv1alpha2.InformerScope
	isolation     kubebindv1alpha2.Isolation
	kubeManager   *kubernetes.Manager
}

func (r *reconciler) reconcile(ctx context.Context, clusterName string, cl client.Client, req *kubebindv1alpha2.BindableResourcesRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Determine the secret name and key to use for the binding response
	var secretName, secretKey string
	if req.Spec.KubeconfigSecretRef != nil {
		// Use the specified secret reference
		secretName = req.Spec.KubeconfigSecretRef.Name
		secretKey = req.Spec.KubeconfigSecretRef.Key
		if secretKey == "" {
			secretKey = "kubeconfig"
		}
	} else {
		// Create a new secret with default naming
		secretName = req.Name + "-binding-response"
		secretKey = "binding-response"
	}

	// Update status with the secret reference
	req.Status.KubeconfigSecretRef = &kubebindv1alpha2.LocalSecretKeyRef{
		Name: secretName,
		Key:  secretKey,
	}

	// Get the template if specified
	var template kubebindv1alpha2.APIServiceExportTemplate
	if req.Spec.TemplateRef.Name != "" {
		if err := cl.Get(ctx, types.NamespacedName{Name: req.Spec.TemplateRef.Name}, &template); err != nil {
			if errors.IsNotFound(err) {
				req.Status.Phase = kubebindv1alpha2.BindableResourcesRequestPhaseFailed
				now := metav1.Now()
				req.Status.CompletionTime = &now
				meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
					Type:               string(kubebindv1alpha2.BindableResourcesRequestConditionReady),
					Status:             metav1.ConditionFalse,
					Reason:             "TemplateNotFound",
					Message:            fmt.Sprintf("APIServiceExportTemplate %q not found", req.Spec.TemplateRef.Name),
					LastTransitionTime: now,
				})
				return ctrl.Result{}, nil // Don't retry - template doesn't exist
			}
			return ctrl.Result{}, fmt.Errorf("failed to get template %q: %w", req.Spec.TemplateRef.Name, err)
		}
	}

	// Default the pretty name to the request name if not provided.
	clusterPrettyName := req.Spec.ClusterIdentity.PrettyName
	if clusterPrettyName == "" {
		clusterPrettyName = req.Name
	}

	// Handle resources and get kubeconfig
	result, err := r.kubeManager.HandleResources(ctx, req.Spec.Author, req.Spec.ClusterIdentity.Identity, clusterName, clusterPrettyName)
	if err != nil {
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               string(kubebindv1alpha2.BindableResourcesRequestConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             "HandleResourcesFailed",
			Message:            fmt.Sprintf("Failed to handle resources: %v", err),
			LastTransitionTime: metav1.Now(),
		})
		return ctrl.Result{}, fmt.Errorf("failed to handle resources for cluster identity %q: %w", req.Spec.ClusterIdentity.Identity, err)
	}

	// Set the namespace in the status
	req.Status.Namespace = result.Namespace

	// Create or update the BindingResourceResponse secret
	if err := r.ensureBindingResponseSecret(ctx, cl, req, result, secretName, secretKey); err != nil {
		meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
			Type:               string(kubebindv1alpha2.BindableResourcesRequestConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             "SecretCreationFailed",
			Message:            fmt.Sprintf("Failed to create/update secret: %v", err),
			LastTransitionTime: metav1.Now(),
		})
		return ctrl.Result{}, fmt.Errorf("failed to ensure binding response secret: %w", err)
	}

	// Set success status
	now := metav1.Now()
	req.Status.Phase = kubebindv1alpha2.BindableResourcesRequestPhaseSucceeded
	req.Status.CompletionTime = &now
	meta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
		Type:               string(kubebindv1alpha2.BindableResourcesRequestConditionReady),
		Status:             metav1.ConditionTrue,
		Reason:             "SecretReady",
		Message:            fmt.Sprintf("Binding response secret %q is ready", secretName),
		LastTransitionTime: now,
	})

	logger.Info("BindableResourcesRequest succeeded", "name", req.Name, "namespace", req.Namespace, "secret", secretName)

	// If TTL is set, requeue for deletion
	if req.Spec.TTLAfterFinished != nil {
		return ctrl.Result{RequeueAfter: req.Spec.TTLAfterFinished.Duration}, nil
	}

	return ctrl.Result{}, nil
}

// with only the kubeconfig, providerID, and providerNamespace set (no authentication or requests).
func (r *reconciler) ensureBindingResponseSecret(
	ctx context.Context,
	cl client.Client,
	req *kubebindv1alpha2.BindableResourcesRequest,
	result *kubernetes.HandleResourcesResult,
	secretName string,
	secretKey string,
) error {
	logger := log.FromContext(ctx)

	// Create the BindingResourceResponse
	response := kubebindv1alpha2.BindingResourceResponse{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kubebindv1alpha2.SchemeGroupVersion.String(),
			Kind:       "BindingResourceResponse",
		},
		Kubeconfig:        result.Kubeconfig,
		ProviderNamespace: result.Namespace,
		ProviderID:        result.ProviderID,
	}

	responseBytes, err := json.Marshal(&response)
	if err != nil {
		return fmt.Errorf("failed to marshal BindingResourceResponse: %w", err)
	}

	// Check if secret already exists
	existingSecret := &corev1.Secret{}
	err = cl.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: secretName}, existingSecret)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get existing secret: %w", err)
	}

	// Build owner reference - the secret is owned by the BindableResourcesRequest
	ownerRef := metav1.OwnerReference{
		APIVersion: kubebindv1alpha2.SchemeGroupVersion.String(),
		Kind:       "BindableResourcesRequest",
		Name:       req.Name,
		UID:        req.UID,
		Controller: func() *bool { b := true; return &b }(),
	}

	if errors.IsNotFound(err) {
		// Create new secret
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            secretName,
				Namespace:       req.Namespace,
				OwnerReferences: []metav1.OwnerReference{ownerRef},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				secretKey: responseBytes,
			},
		}
		logger.Info("Creating BindingResourceResponse secret", "name", secretName, "namespace", req.Namespace, "key", secretKey)
		if err := cl.Create(ctx, secret); err != nil {
			if errors.IsAlreadyExists(err) {
				return nil // Race condition, secret was created
			}
			return fmt.Errorf("failed to create secret: %w", err)
		}
	} else {
		// Update existing secret
		// Ensure owner reference is set
		hasOwnerRef := false
		for _, ref := range existingSecret.OwnerReferences {
			if ref.UID == req.UID {
				hasOwnerRef = true
				break
			}
		}
		if !hasOwnerRef {
			existingSecret.OwnerReferences = append(existingSecret.OwnerReferences, ownerRef)
		}

		// Update data if changed
		if existingSecret.Data == nil {
			existingSecret.Data = make(map[string][]byte)
		}
		if string(existingSecret.Data[secretKey]) != string(responseBytes) {
			existingSecret.Data[secretKey] = responseBytes
			logger.Info("Updating BindingResourceResponse secret", "name", secretName, "namespace", req.Namespace, "key", secretKey)
			if err := cl.Update(ctx, existingSecret); err != nil {
				return fmt.Errorf("failed to update secret: %w", err)
			}
		}
	}

	return nil
}
