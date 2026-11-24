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

package kubernetes

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	authorizationv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	kuberesources "github.com/kube-bind/kube-bind/backend/kubernetes/resources"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
)

type Manager struct {
	namespacePrefix    string
	providerPrettyName string
	scope              kubebindv1alpha2.InformerScope

	externalAddressGenerator kuberesources.ExternalAddressGeneratorFunc
	externalCA               []byte
	externalTLSServerName    string

	manager      mcmanager.Manager
	embeddedOIDC bool

	// konnectorHostAliases are default host aliases injected into konnector
	// pods deployed via the UI flow (configured via --konnector-host-alias flag).
	konnectorHostAliases []corev1.HostAlias
}

func NewKubernetesManager(
	ctx context.Context,
	namespacePrefix, providerPrettyName string,
	externalAddressGenerator kuberesources.ExternalAddressGeneratorFunc,
	scope kubebindv1alpha2.InformerScope,
	externalCA []byte,
	externalTLSServerName string,
	manager mcmanager.Manager,
	embeddedOIDC bool,
	konnectorHostAliases []corev1.HostAlias,
) (*Manager, error) {
	m := &Manager{
		namespacePrefix:    namespacePrefix,
		providerPrettyName: providerPrettyName,
		scope:              scope,

		externalAddressGenerator: externalAddressGenerator,
		externalCA:               externalCA,
		externalTLSServerName:    externalTLSServerName,

		manager:              manager,
		embeddedOIDC:         embeddedOIDC,
		konnectorHostAliases: konnectorHostAliases,
	}

	if err := m.manager.GetFieldIndexer().IndexField(ctx, &corev1.Namespace{}, NamespacesByIdentity,
		IndexNamespacesByIdentity); err != nil {
		return nil, fmt.Errorf("failed to setup NamespacesByIdentity indexer: %w", err)
	}

	return m, nil
}

// HandleResourcesResult contains the result of HandleResources operation.
type HandleResourcesResult struct {
	// Kubeconfig is the kubeconfig data for accessing the service provider cluster.
	Kubeconfig []byte
	// Namespace is the namespace assigned to this binding on the service provider cluster.
	Namespace string
	// ProviderID is the UID of the kube-system namespace of the provider cluster.
	ProviderID string
}

func (m *Manager) HandleResources(
	ctx context.Context,
	author, identity, cluster, clusterPrettyName string,
) (*HandleResourcesResult, error) {
	logger := klog.FromContext(ctx).WithValues("identity", identity)
	ctx = klog.NewContext(ctx, logger)

	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	// try to find an existing namespace by annotation, or create a new one.
	var nss corev1.NamespaceList
	err = c.List(ctx, &nss, client.MatchingFields{NamespacesByIdentity: identity})
	if err != nil {
		return nil, err
	}
	if len(nss.Items) > 1 {
		logger.Error(fmt.Errorf("found multiple namespaces for identity %q", identity), "found multiple namespaces for identity")
		return nil, fmt.Errorf("found multiple namespaces for identity %q", identity)
	}
	var ns string
	if len(nss.Items) == 1 {
		ns = nss.Items[0].Name
	} else {
		nsObj, err := kuberesources.CreateNamespace(ctx, c, m.namespacePrefix, identity, author)
		if err != nil {
			return nil, err
		}
		logger.Info("Created namespace", "namespace", nsObj.Name)
		ns = nsObj.Name
	}
	logger = logger.WithValues("namespace", ns)
	ctx = klog.NewContext(ctx, logger)

	// first look for ClusterBinding to get old secret name
	kubeconfigSecretName := kuberesources.KubeconfigSecretName
	var cb kubebindv1alpha2.ClusterBinding
	err = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: kuberesources.ClusterBindingName}, &cb)
	switch {
	case errors.IsNotFound(err):
		if err := kuberesources.CreateClusterBinding(ctx, c, ns, "kubeconfig", m.providerPrettyName, identity, author, clusterPrettyName); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		logger.V(3).Info("Found existing ClusterBinding")
		kubeconfigSecretName = cb.Spec.KubeconfigSecretRef.Name // reuse old name
	}

	sa, err := kuberesources.CreateServiceAccount(ctx, c, ns, kuberesources.ServiceAccountName)
	if err != nil {
		return nil, err
	}

	if err := kuberesources.EnsureBinderClusterRole(ctx, c); err != nil {
		return nil, err
	}

	saSecret, err := kuberesources.CreateSASecret(ctx, c, ns, sa.Name)
	if err != nil {
		return nil, err
	}

	if err = kuberesources.EnsureBinderRoleBinding(ctx, c, ns, sa.Name); err != nil {
		return nil, err
	}

	kfgSecret, err := kuberesources.GenerateKubeconfig(ctx, c, cl.GetConfig(), m.externalAddressGenerator, m.externalCA, m.externalTLSServerName, saSecret.Name, ns, kubeconfigSecretName)
	if err != nil {
		return nil, err
	}

	var kubeSystemNS corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: "kube-system"}, &kubeSystemNS); err != nil {
		return nil, fmt.Errorf("failed to get kube-system namespace for ProviderID: %w", err)
	}

	return &HandleResourcesResult{
		Kubeconfig: kfgSecret.Data["kubeconfig"],
		Namespace:  ns,
		ProviderID: string(kubeSystemNS.UID),
	}, nil
}

// CreateAPIServiceExportRequest creates an APIServiceExportRequest in the given namespace
// on the provider cluster and waits for it to be reconciled (Succeeded or Failed).
func (m *Manager) CreateAPIServiceExportRequest(
	ctx context.Context,
	cluster, namespace, name string,
	spec kubebindv1alpha2.APIServiceExportRequestSpec,
) (*kubebindv1alpha2.APIServiceExportRequest, error) {
	logger := klog.FromContext(ctx).WithValues("namespace", namespace, "name", name)

	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster client: %w", err)
	}
	c := cl.GetClient()

	exportRequest := &kubebindv1alpha2.APIServiceExportRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}

	// Create the APIServiceExportRequest, handling name conflicts
	if err := c.Create(ctx, exportRequest); err != nil {
		if !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create APIServiceExportRequest: %w", err)
		}
		// Name conflict: use generateName
		exportRequest.Name = ""
		exportRequest.GenerateName = name + "-"
		if err := c.Create(ctx, exportRequest); err != nil {
			return nil, fmt.Errorf("failed to create APIServiceExportRequest with generated name: %w", err)
		}
	}

	createdName := exportRequest.Name
	logger = logger.WithValues("createdName", createdName)
	logger.Info("Created APIServiceExportRequest, waiting for reconciliation")

	// Poll until reconciled. The client reads from cache, so the object may not
	// be visible immediately after creation. Tolerate NotFound for an initial
	// grace period before treating it as a real deletion.
	var result *kubebindv1alpha2.APIServiceExportRequest
	seenOnce := false
	if err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 60*time.Second, false, func(ctx context.Context) (bool, error) {
		req := &kubebindv1alpha2.APIServiceExportRequest{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: createdName}, req); err != nil {
			if errors.IsNotFound(err) {
				if seenOnce {
					return false, fmt.Errorf("APIServiceExportRequest %s was deleted", createdName)
				}
				// Cache hasn't synced yet — keep polling.
				return false, nil
			}
			return false, err
		}
		seenOnce = true
		if req.Status.Phase == kubebindv1alpha2.APIServiceExportRequestPhaseSucceeded {
			result = req
			return true, nil
		}
		if req.Status.Phase == kubebindv1alpha2.APIServiceExportRequestPhaseFailed {
			return false, fmt.Errorf("APIServiceExportRequest failed: %s", req.Status.TerminalMessage)
		}
		return false, nil
	}); err != nil {
		return nil, fmt.Errorf("waiting for APIServiceExportRequest: %w", err)
	}

	logger.Info("APIServiceExportRequest reconciled successfully")
	return result, nil
}

func (m *Manager) ListCustomResourceDefinitions(ctx context.Context, cluster string, selector labels.Selector) (*apiextensionsv1.CustomResourceDefinitionList, error) {
	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	var crds apiextensionsv1.CustomResourceDefinitionList
	err = c.List(ctx, &crds, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return nil, err
	}

	return &crds, nil
}

func (m *Manager) ListCollections(ctx context.Context, cluster string) (*kubebindv1alpha2.CollectionList, error) {
	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	var collections kubebindv1alpha2.CollectionList
	err = c.List(ctx, &collections)
	if err != nil {
		return nil, err
	}

	return &collections, nil
}

func (m *Manager) ListTemplates(ctx context.Context, cluster string) (*kubebindv1alpha2.APIServiceExportTemplateList, error) {
	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	var templates kubebindv1alpha2.APIServiceExportTemplateList
	err = c.List(ctx, &templates)
	if err != nil {
		return nil, err
	}

	return &templates, nil
}

func (m *Manager) GetTemplates(ctx context.Context, cluster, name string) (*kubebindv1alpha2.APIServiceExportTemplate, error) {
	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	var template kubebindv1alpha2.APIServiceExportTemplate
	err = c.Get(ctx, types.NamespacedName{Name: name}, &template)
	if err != nil {
		return nil, err
	}

	return &template, nil
}

func (m *Manager) ListDynamicResources(ctx context.Context, cluster string, gvk schema.GroupVersionKind, selector labels.Selector) (*unstructured.UnstructuredList, error) {
	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	// Ensure we have the List kind
	listGVK := gvk
	if !strings.HasSuffix(listGVK.Kind, "List") {
		listGVK.Kind += "List"
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listGVK)

	listOpts := []client.ListOption{}
	if selector != nil {
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
	}

	if err := c.List(ctx, list, listOpts...); err != nil {
		return nil, err
	}

	return list, nil
}

func (m *Manager) AuthorizeRequest(ctx context.Context, subject string, groups []string, cluster, method, path string) error {
	logger := klog.FromContext(ctx).WithValues("subject", subject, "cluster", cluster, "method", method, "path", path, "groups", groups)

	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return err
	}
	cfg := cl.GetConfig()

	// Create the authorization clientset
	authClient, err := authorizationv1.NewForConfig(cfg)
	if err != nil {
		return err
	}

	if m.embeddedOIDC {
		groups = append(groups, "system:authenticated")
	}

	// Check if user can bind (basic permission test)
	sar := &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			User:   subject,
			Groups: groups,
			ResourceAttributes: &authzv1.ResourceAttributes{
				Verb:  "bind",
				Group: "kube-bind.io",
			},
		},
	}
	// Perform the SubjectAccessReview using the specific clientset
	sarResponse, err := authClient.SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		logger.Error(err, "Failed to create SubjectAccessReview")
		return err
	}

	if !sarResponse.Status.Allowed {
		logger.Info("User is not allowed to bind", "user", subject, "reason", sarResponse.Status)
		// Return a structured authorization error
		return errors.NewForbidden(
			schema.GroupResource{Group: "kube-bind.io", Resource: "bind"},
			"",
			fmt.Errorf("user %q is not authorized to bind resources: %s", subject, sarResponse.Status.Reason),
		)
	}
	logger.Info("User is allowed to bind", "user", subject)
	return nil
}

// ConsumerStatus describes the status of a consumer cluster relative to the provider.
type ConsumerStatus struct {
	// Connected is true if the consumer has an existing provider namespace.
	Connected bool `json:"connected"`
	// Namespace is the provider namespace for this consumer.
	Namespace string `json:"namespace,omitempty"`
	// Exports is the list of APIServiceExport names in the consumer's namespace.
	Exports []string `json:"exports,omitempty"`
}

// GetConsumerStatus checks if the given identity already has a provider namespace
// with existing APIServiceExports.
func (m *Manager) GetConsumerStatus(ctx context.Context, identity, cluster string) (*ConsumerStatus, error) {
	logger := klog.FromContext(ctx).WithValues("identity", identity)

	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, err
	}
	c := cl.GetClient()

	// Look up namespace by identity annotation
	var nss corev1.NamespaceList
	err = c.List(ctx, &nss, client.MatchingFields{NamespacesByIdentity: identity})
	if err != nil {
		return nil, err
	}

	if len(nss.Items) == 0 {
		return &ConsumerStatus{Connected: false}, nil
	}

	ns := nss.Items[0].Name
	logger.Info("Found existing consumer namespace", "namespace", ns)

	// List APIServiceExports in the consumer namespace
	var exports kubebindv1alpha2.APIServiceExportList
	if err := c.List(ctx, &exports, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("failed to list APIServiceExports: %w", err)
	}

	exportNames := make([]string, 0, len(exports.Items))
	for _, e := range exports.Items {
		exportNames = append(exportNames, e.Name)
	}

	return &ConsumerStatus{
		Connected: len(exportNames) > 0,
		Namespace: ns,
		Exports:   exportNames,
	}, nil
}

// ApplyToConsumerResult contains the result of ApplyToConsumer operation.
type ApplyToConsumerResult struct {
	KonnectorDeployed bool   `json:"konnectorDeployed"`
	BundleCreated     bool   `json:"bundleCreated"`
	Message           string `json:"message"`
}

// ApplyToConsumer applies konnector manifests and binding bundle to a consumer cluster
// using the provided consumer kubeconfig.
func (m *Manager) ApplyToConsumer(
	ctx context.Context,
	consumerKubeconfigData []byte,
	providerKubeconfigData []byte,
	bindingName string,
	konnectorImage string,
	overrideHostAliases []corev1.HostAlias,
) (*ApplyToConsumerResult, error) {
	logger := klog.FromContext(ctx).WithValues("bindingName", bindingName)

	// Build a client from the consumer kubeconfig
	consumerConfig, err := clientcmd.RESTConfigFromKubeConfig(consumerKubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("invalid consumer kubeconfig: %w", err)
	}

	s := scheme.Scheme
	if err := kubebindv1alpha2.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("failed to add kube-bind scheme: %w", err)
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("failed to add apiextensions scheme: %w", err)
	}

	consumerClient, err := client.New(consumerConfig, client.Options{Scheme: s})
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer client: %w", err)
	}

	// 1. Ensure kube-bind namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-bind"},
	}
	if err := consumerClient.Create(ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("failed to create kube-bind namespace: %w", err)
	}

	// 2. Build host aliases: start with request overrides (if any), fall back to
	// configured defaults, then merge any auto-resolved aliases from the provider kubeconfig.
	var hostAliases []corev1.HostAlias
	if len(overrideHostAliases) > 0 {
		hostAliases = append(hostAliases, overrideHostAliases...)
	} else {
		hostAliases = append(hostAliases, m.konnectorHostAliases...)
	}
	if resolved := m.resolveProviderHostAliases(ctx, providerKubeconfigData); len(resolved) > 0 {
		hostAliases = mergeHostAliases(hostAliases, resolved)
	}

	// 3. Deploy konnector (idempotent)
	konnectorDeployed, err := m.ensureKonnector(ctx, consumerClient, konnectorImage, hostAliases)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy konnector: %w", err)
	}

	// 3b. If konnector was newly deployed, wait for it to bootstrap CRDs
	if konnectorDeployed {
		logger.Info("Waiting for konnector to bootstrap CRDs on consumer cluster")
		if err := m.waitForCRD(ctx, consumerClient, "apiservicebindingbundles.kube-bind.io"); err != nil {
			return nil, fmt.Errorf("timed out waiting for konnector CRDs: %w", err)
		}
	}

	// 4. Create kubeconfig secret for the provider
	secretName := fmt.Sprintf("kubeconfig-%s", bindingName)
	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "kube-bind",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"kubeconfig": providerKubeconfigData,
		},
	}
	if err := consumerClient.Create(ctx, kubeconfigSecret); err != nil {
		if errors.IsAlreadyExists(err) {
			// Update existing secret
			existing := &corev1.Secret{}
			if err := consumerClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "kube-bind"}, existing); err != nil {
				return nil, fmt.Errorf("failed to get existing kubeconfig secret: %w", err)
			}
			existing.Data = kubeconfigSecret.Data
			if err := consumerClient.Update(ctx, existing); err != nil {
				return nil, fmt.Errorf("failed to update kubeconfig secret: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create kubeconfig secret: %w", err)
		}
	}

	// 5. Create APIServiceBindingBundle
	bundle := &kubebindv1alpha2.APIServiceBindingBundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: bindingName,
		},
		Spec: kubebindv1alpha2.APIServiceBindingBundleSpec{
			KubeconfigSecretRef: kubebindv1alpha2.ClusterSecretKeyRef{
				LocalSecretKeyRef: kubebindv1alpha2.LocalSecretKeyRef{
					Name: secretName,
					Key:  "kubeconfig",
				},
				Namespace: "kube-bind",
			},
		},
	}
	if err := consumerClient.Create(ctx, bundle); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("APIServiceBindingBundle already exists, updating")
			existing := &kubebindv1alpha2.APIServiceBindingBundle{}
			if err := consumerClient.Get(ctx, types.NamespacedName{Name: bindingName}, existing); err != nil {
				return nil, fmt.Errorf("failed to get existing bundle: %w", err)
			}
			existing.Spec = bundle.Spec
			if err := consumerClient.Update(ctx, existing); err != nil {
				return nil, fmt.Errorf("failed to update bundle: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create APIServiceBindingBundle: %w", err)
		}
	}

	logger.Info("Successfully applied binding to consumer cluster")
	return &ApplyToConsumerResult{
		KonnectorDeployed: konnectorDeployed,
		BundleCreated:     true,
		Message:           "Konnector and binding bundle applied successfully",
	}, nil
}

// resolveProviderHostAliases extracts the provider API server hostname from the
// kubeconfig and resolves it to IP addresses. This allows the konnector pods on
// the consumer cluster to reach the provider even when the hostname only resolves
// correctly from the backend pod's network (e.g., Kind/Docker environments).
func (m *Manager) resolveProviderHostAliases(ctx context.Context, providerKubeconfigData []byte) []corev1.HostAlias {
	config, err := clientcmd.RESTConfigFromKubeConfig(providerKubeconfigData)
	if err != nil {
		return nil
	}

	u, err := url.Parse(config.Host)
	if err != nil {
		return nil
	}

	hostname := u.Hostname()
	if hostname == "" {
		return nil
	}

	if net.ParseIP(hostname) != nil {
		return nil
	}

	ips, err := net.DefaultResolver.LookupHost(ctx, hostname)
	if err != nil || len(ips) == 0 {
		return nil
	}

	var validIPs []string
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed != nil && !parsed.IsLoopback() {
			validIPs = append(validIPs, ip)
		}
	}

	if len(validIPs) == 0 {
		return nil
	}

	return []corev1.HostAlias{
		{
			IP:        validIPs[0],
			Hostnames: []string{hostname},
		},
	}
}

// ensureKonnector deploys the konnector agent to the consumer cluster.
// Returns true if the konnector was newly deployed, false if it already existed.
func (m *Manager) ensureKonnector(ctx context.Context, c client.Client, konnectorImage string, hostAliases []corev1.HostAlias) (bool, error) {
	manifests := kuberesources.NewKonnectorManifests(konnectorImage, hostAliases)

	// Check if konnector deployment already exists
	existing := &appsv1.Deployment{}
	err := c.Get(ctx, types.NamespacedName{Name: kuberesources.KonnectorDeploymentName, Namespace: kuberesources.KonnectorNamespace}, existing)
	if err == nil {
		// Update the deployment if host aliases changed
		existing.Spec.Template.Spec.HostAliases = hostAliases
		existing.Spec.Template.Spec.Containers[0].Image = konnectorImage
		if err := c.Update(ctx, existing); err != nil {
			return false, fmt.Errorf("failed to update konnector deployment: %w", err)
		}
		return false, nil
	}
	if !errors.IsNotFound(err) {
		return false, fmt.Errorf("failed to check for existing konnector: %w", err)
	}

	if err := c.Create(ctx, manifests.ServiceAccount); err != nil && !errors.IsAlreadyExists(err) {
		return false, fmt.Errorf("failed to create konnector service account: %w", err)
	}
	if err := c.Create(ctx, manifests.ClusterRole); err != nil && !errors.IsAlreadyExists(err) {
		return false, fmt.Errorf("failed to create konnector cluster role: %w", err)
	}
	if err := c.Create(ctx, manifests.ClusterRoleBinding); err != nil && !errors.IsAlreadyExists(err) {
		return false, fmt.Errorf("failed to create konnector cluster role binding: %w", err)
	}
	if err := c.Create(ctx, manifests.Deployment); err != nil {
		return false, fmt.Errorf("failed to create konnector deployment: %w", err)
	}

	return true, nil
}

func (m *Manager) waitForCRD(ctx context.Context, c client.Client, crdName string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 120*time.Second, true, func(ctx context.Context) (bool, error) {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		err := c.Get(ctx, types.NamespacedName{Name: crdName}, crd)
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil // keep polling
			}
			return false, nil // transient error, keep polling
		}
		// Check if the CRD is established
		for _, cond := range crd.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func (m *Manager) SeedDefaultCluster(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	cl, err := m.manager.GetCluster(ctx, multicluster.ClusterName(""))
	if err != nil {
		return err
	}
	c := cl.GetClient()

	_, err = kuberesources.CreateDefaultCluster(ctx, c)
	if err != nil && !errors.IsAlreadyExists(err) {
		logger.Error(err, "Failed to create default Cluster resource")
		return err
	}
	logger.Info("Default Cluster resource ensured")
	return nil
}

// GetKonnectorHostAliases returns the configured default host aliases for konnector pods.
func (m *Manager) GetKonnectorHostAliases() []corev1.HostAlias {
	return m.konnectorHostAliases
}

// mergeHostAliases merges additional host aliases into existing ones,
// skipping entries whose IP is already present.
func mergeHostAliases(existing, additional []corev1.HostAlias) []corev1.HostAlias {
	seen := make(map[string]bool, len(existing))
	for _, ha := range existing {
		seen[ha.IP] = true
	}
	for _, ha := range additional {
		if !seen[ha.IP] {
			existing = append(existing, ha)
			seen[ha.IP] = true
		}
	}
	return existing
}
