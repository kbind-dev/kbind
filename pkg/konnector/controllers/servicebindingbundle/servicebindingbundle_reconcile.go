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

package servicebindingbundle

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	conditionsapi "github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/util/conditions"
	bindclient "github.com/kube-bind/kube-bind/sdk/client/clientset/versioned"
)

const (
	// APIServiceBindingBundleConditionSecretValid is set when the secret is valid.
	APIServiceBindingBundleConditionSecretValid conditionsapi.ConditionType = "SecretValid"

	// APIServiceBindingBundleConditionSynced is set when all APIServiceBindings are synced.
	APIServiceBindingBundleConditionSynced conditionsapi.ConditionType = "Synced"
)

type reconciler struct {
	providerPollingInternaval time.Duration
	getConsumerSecret         func(ns, name string) (*corev1.Secret, error)
	listAPIServiceBindings    func() ([]*kubebindv1alpha2.APIServiceBinding, error)
	consumerBindClient        bindclient.Interface
}

func (r *reconciler) reconcile(ctx context.Context, bundle *kubebindv1alpha2.APIServiceBindingBundle) error {
	var errs []error

	// First validate the kubeconfig secret
	providerConfig, providerNamespace, err := r.ensureValidKubeconfigSecret(ctx, bundle)
	if err != nil {
		errs = append(errs, err)
	}

	// Only proceed to sync bindings if we have a valid kubeconfig
	if providerConfig != nil {
		if err := r.syncAPIServiceBindings(ctx, bundle, providerConfig, providerNamespace); err != nil {
			errs = append(errs, err)
		}
	}

	conditions.SetSummary(bundle)

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureValidKubeconfigSecret(_ context.Context, bundle *kubebindv1alpha2.APIServiceBindingBundle) (*rest.Config, string, error) {
	secret, err := r.getConsumerSecret(bundle.Spec.KubeconfigSecretRef.Namespace, bundle.Spec.KubeconfigSecretRef.Name)
	if err != nil && !errors.IsNotFound(err) {
		return nil, "", err
	} else if errors.IsNotFound(err) {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSecretValid,
			"KubeconfigSecretNotFound",
			conditionsapi.ConditionSeverityError,
			"Kubeconfig secret %s/%s not found.",
			bundle.Spec.KubeconfigSecretRef.Namespace, bundle.Spec.KubeconfigSecretRef.Name,
		)
		return nil, "", nil
	}

	kubeconfig, found := secret.Data[bundle.Spec.KubeconfigSecretRef.Key]
	if !found {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSecretValid,
			"KubeconfigSecretInvalid",
			conditionsapi.ConditionSeverityError,
			"Kubeconfig secret %s/%s is missing %q string key.",
			bundle.Spec.KubeconfigSecretRef.Namespace,
			bundle.Spec.KubeconfigSecretRef.Name,
			bundle.Spec.KubeconfigSecretRef.Key,
		)
		return nil, "", nil
	}

	cfg, err := clientcmd.Load(kubeconfig)
	if err != nil {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSecretValid,
			"KubeconfigSecretInvalid",
			conditionsapi.ConditionSeverityError,
			"Kubeconfig secret %s/%s has an invalid kubeconfig: %v",
			bundle.Spec.KubeconfigSecretRef.Namespace,
			bundle.Spec.KubeconfigSecretRef.Name,
			err,
		)
		return nil, "", nil
	}
	kubeContext, found := cfg.Contexts[cfg.CurrentContext]
	if !found {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSecretValid,
			"KubeconfigSecretInvalid",
			conditionsapi.ConditionSeverityError,
			"Kubeconfig secret %s/%s has an invalid kubeconfig: current context %q not found",
			bundle.Spec.KubeconfigSecretRef.Namespace,
			bundle.Spec.KubeconfigSecretRef.Name,
			cfg.CurrentContext,
		)
		return nil, "", nil
	}
	if kubeContext.Namespace == "" {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSecretValid,
			"KubeconfigSecretInvalid",
			conditionsapi.ConditionSeverityError,
			"Kubeconfig secret %s/%s has an invalid kubeconfig: current context %q has no namespace set",
			bundle.Spec.KubeconfigSecretRef.Namespace,
			bundle.Spec.KubeconfigSecretRef.Name,
			cfg.CurrentContext,
		)
		return nil, "", nil
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSecretValid,
			"KubeconfigSecretInvalid",
			conditionsapi.ConditionSeverityError,
			"Kubeconfig secret %s/%s has an invalid kubeconfig: %v",
			bundle.Spec.KubeconfigSecretRef.Namespace,
			bundle.Spec.KubeconfigSecretRef.Name,
			err,
		)
		return nil, "", nil
	}

	conditions.MarkTrue(
		bundle,
		APIServiceBindingBundleConditionSecretValid,
	)

	return restConfig, kubeContext.Namespace, nil
}

func (r *reconciler) syncAPIServiceBindings(ctx context.Context, bundle *kubebindv1alpha2.APIServiceBindingBundle, providerConfig *rest.Config, providerNamespace string) error {
	logger := klog.FromContext(ctx)

	// Create a client for the provider cluster
	providerClient, err := bindclient.NewForConfig(providerConfig)
	if err != nil {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSynced,
			"ProviderClientError",
			conditionsapi.ConditionSeverityError,
			"Failed to create provider cluster client: %v",
			err,
		)
		return err
	}

	// Create a standard kubernetes client to get the kube-system namespace UID
	kubeClient, err := kubernetes.NewForConfig(providerConfig)
	if err != nil {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSynced,
			"ProviderClientError",
			conditionsapi.ConditionSeverityError,
			"Failed to create provider core client: %v",
			err,
		)
		return err
	}

	var providerID string
	if kubeSystemNs, err := kubeClient.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{}); err == nil {
		providerID = string(kubeSystemNs.UID)
	} else {
		logger.Error(err, "Failed to get kube-system namespace from provider, ProviderID will be empty")
	}

	// List all APIServiceExports from the provider cluster
	exports, err := providerClient.KubeBindV1alpha2().APIServiceExports(providerNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSynced,
			"ListExportsFailed",
			conditionsapi.ConditionSeverityError,
			"Failed to list APIServiceExports from provider cluster: %v",
			err,
		)
		return err
	}

	logger.V(2).Info("Found APIServiceExports in provider cluster", "count", len(exports.Items))

	// Get existing APIServiceBindings owned by this bundle
	allBindings, err := r.listAPIServiceBindings()
	if err != nil {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSynced,
			"ListBindingsFailed",
			conditionsapi.ConditionSeverityError,
			"Failed to list APIServiceBindings: %v",
			err,
		)
		return err
	}

	// Filter bindings owned by this bundle
	ownedBindings := make(map[string]*kubebindv1alpha2.APIServiceBinding)
	for _, binding := range allBindings {
		for _, ownerRef := range binding.OwnerReferences {
			if ownerRef.APIVersion == kubebindv1alpha2.SchemeGroupVersion.String() &&
				ownerRef.Kind == "APIServiceBindingBundle" &&
				ownerRef.Name == bundle.Name &&
				ownerRef.UID == bundle.UID {
				ownedBindings[binding.Name] = binding
				break
			}
		}
	}

	// Create or update APIServiceBindings for each export
	var errs []error
	expectedBindings := make(map[string]bool)
	for _, export := range exports.Items {
		// Generate a unique name for the binding based on bundle name and export
		expectedBindings[export.Name] = true

		if existingBinding, exists := ownedBindings[export.Name]; exists {
			// Binding already exists, check if it needs update
			logger.V(2).Info("APIServiceBinding already exists", "binding", export.Name)
			// Check if kubeconfig secret ref matches
			if existingBinding.Spec.KubeconfigSecretRef != bundle.Spec.KubeconfigSecretRef {
				// Update is needed but spec is immutable, so we skip this
				logger.V(2).Info("APIServiceBinding spec differs but is immutable", "binding", export.Name)
			}
			continue
		}

		// Create new APIServiceBinding
		binding := &kubebindv1alpha2.APIServiceBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: export.Name,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: kubebindv1alpha2.SchemeGroupVersion.String(),
						Kind:       "APIServiceBindingBundle",
						Name:       bundle.Name,
						UID:        bundle.UID,
						Controller: func() *bool { b := true; return &b }(),
					},
				},
			},
			Spec: kubebindv1alpha2.APIServiceBindingSpec{
				KubeconfigSecretRef: bundle.Spec.KubeconfigSecretRef,
				ProviderID:          providerID,
				IsDefault:           true,
			},
		}

		logger.Info("Creating APIServiceBinding", "binding", export.Name, "export", fmt.Sprintf("%s/%s", export.Namespace, export.Name))
		if _, err := r.consumerBindClient.KubeBindV1alpha2().APIServiceBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			if !errors.IsAlreadyExists(err) {
				logger.Error(err, "Failed to create APIServiceBinding", "binding", export.Name)
				errs = append(errs, err)
			}
		}
	}

	// Delete orphaned APIServiceBindings (those owned by this bundle but no longer have a corresponding export)
	for bindingName, binding := range ownedBindings {
		if !expectedBindings[bindingName] {
			logger.Info("Deleting orphaned APIServiceBinding", "binding", bindingName)
			if err := r.consumerBindClient.KubeBindV1alpha2().APIServiceBindings().Delete(ctx, binding.Name, metav1.DeleteOptions{}); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "Failed to delete orphaned APIServiceBinding", "binding", bindingName)
					errs = append(errs, err)
				}
			}
		}
	}

	if len(errs) > 0 {
		conditions.MarkFalse(
			bundle,
			APIServiceBindingBundleConditionSynced,
			"SyncErrors",
			conditionsapi.ConditionSeverityWarning,
			"Failed to sync some APIServiceBindings: %d error(s)",
			len(errs),
		)
		return utilerrors.NewAggregate(errs)
	}

	conditions.MarkTrue(
		bundle,
		APIServiceBindingBundleConditionSynced,
	)

	return nil
}
