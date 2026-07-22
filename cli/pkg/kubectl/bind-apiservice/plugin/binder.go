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

package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/kube-bind/kube-bind/cli/pkg/kubectl/base"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
)

// BinderOptions contains the configuration for the shared binder
type BinderOptions struct {
	IOStreams                    genericclioptions.IOStreams
	SkipKonnector                bool
	KonnectorImageOverride       string
	KonnectorHostAliasParsed     []corev1.HostAlias
	DowngradeKonnector           bool
	RemoteKubeconfigFile         string
	RemoteKubeconfigNamespace    string
	RemoteKubeconfigName         string
	RemoteNamespace              string
	ClusterIdentity              string
	ClusterIdentityNamespaceName string
	File                         string
	DryRun                       bool
}

// Binder provides shared binding functionality for both bind and bind-apiservice commands
type Binder struct {
	opts   *BinderOptions
	config *rest.Config
}

// NewBinder creates a new shared binder instance
func NewBinder(config *rest.Config, opts *BinderOptions) *Binder {
	return &Binder{
		config: config,
		opts:   opts,
	}
}

// TODO: bindFromFile and bindFromResponse can likely share a lot of code. This slow is bit repetitive
// but keeps the two paths separate for clarity. But it needs love.
// https://github.com/kube-bind/kube-bind/issues/360

func (b *Binder) BindFromFile(ctx context.Context) ([]*kubebindv1alpha2.APIServiceBinding, error) {
	// Generate the kubectl command that would be equivalent
	remoteFlags := ""
	if b.opts.RemoteKubeconfigFile != "" {
		remoteFlags = fmt.Sprintf("--remote-kubeconfig %s", b.opts.RemoteKubeconfigFile)
	} else if b.opts.RemoteKubeconfigNamespace != "" && b.opts.RemoteKubeconfigName != "" {
		remoteFlags = fmt.Sprintf("--remote-kubeconfig-namespace %s --remote-kubeconfig-name %s", b.opts.RemoteKubeconfigNamespace, b.opts.RemoteKubeconfigName)
	}
	fmt.Fprintf(b.opts.IOStreams.ErrOut, "🚀 Executing: kubectl bind apiservice %s -f -\n", remoteFlags)
	fmt.Fprintf(b.opts.IOStreams.ErrOut, "✨ Use \"-o yaml\" and \"--dry-run\" to get the APIServiceExportRequest.\n")
	fmt.Fprintf(b.opts.IOStreams.ErrOut, "    and pass it to \"kubectl bind apiservice\" directly. Great for automation.\n")

	// Ensure client side namespace exists
	err := b.ensureClientSideNamespaceExists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure kube-bind namespace exists: %w", err)
	}

	remoteKubeconfig, _, _, err := b.getRemoteKubeconfig(ctx, "", "")
	if err != nil {
		return nil, err
	}

	// Copy kubeconfig into local cluster
	remoteHost, remoteNamespace, err := base.ParseRemoteKubeconfig([]byte(remoteKubeconfig))
	if err != nil {
		return nil, err
	}

	kubeClient, err := kubeclient.NewForConfig(b.config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %w", err)
	}

	secretName, err := base.FindRemoteKubeconfig(ctx, kubeClient, remoteNamespace, remoteHost)
	if err != nil {
		return nil, err
	}

	secret, created, err := base.EnsureKubeconfigSecret(ctx, remoteKubeconfig, secretName, kubeClient)
	if err != nil {
		return nil, err
	}

	if created {
		fmt.Fprintf(b.opts.IOStreams.ErrOut, "🔒 Created secret %s/%s for host %s, namespace %s\n", "kube-bind", secret.Name, remoteHost, remoteNamespace)
	} else {
		fmt.Fprintf(b.opts.IOStreams.ErrOut, "🔒 Updated secret %s/%s for host %s, namespace %s\n", "kube-bind", secret.Name, remoteHost, remoteNamespace)
	}

	if b.opts.DryRun {
		return nil, nil
	}

	// Get remote kubeconfig
	remoteKubeconfig, remoteNamespaceActual, remoteConfig, err := b.getRemoteKubeconfig(ctx, secret.Namespace, secret.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote kubeconfig: %w", err)
	}

	data, err := b.getRequestManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to get request manifest: %w", err)
	}

	request, err := b.unmarshalManifest(data)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request manifest: %w", err)
	}

	// Deploy konnector if needed
	if err := b.deployKonnector(ctx); err != nil {
		return nil, fmt.Errorf("failed to deploy konnector: %w", err)
	}

	// Create bindings for all requests
	result, err := b.createServiceExportRequest(ctx, remoteConfig, remoteNamespaceActual, request)
	if err != nil {
		return nil, fmt.Errorf("failed to create service export request: %w", err)
	}

	secretName, err = b.createKubeconfigSecret(ctx, remoteConfig.Host, remoteNamespaceActual, remoteKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig secret: %w", err)
	}

	results, err := b.createAPIServiceBindings(ctx, result, secretName, "", false)
	if err != nil {
		return nil, fmt.Errorf("failed to create API service bindings: %w", err)
	}

	return results, nil
}

// BindFromResponse processes a BindingResourceResponse and creates all necessary bindings
func (b *Binder) BindFromResponse(ctx context.Context, response *kubebindv1alpha2.BindingResourceResponse) ([]*kubebindv1alpha2.APIServiceBinding, error) {
	if response == nil || response.Authentication.OAuth2CodeGrant == nil {
		return nil, fmt.Errorf("unexpected response: authentication.oauth2CodeGrant is nil")
	}

	// Ensure client side namespace exists
	err := b.ensureClientSideNamespaceExists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure kube-bind namespace exists: %w", err)
	}

	// Copy kubeconfig into local cluster
	remoteHost, remoteNamespace, err := base.ParseRemoteKubeconfig(response.Kubeconfig)
	if err != nil {
		return nil, err
	}

	kubeClient, err := kubeclient.NewForConfig(b.config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %w", err)
	}

	secretName, err := base.FindRemoteKubeconfig(ctx, kubeClient, remoteNamespace, remoteHost)
	if err != nil {
		return nil, err
	}

	secret, created, err := base.EnsureKubeconfigSecret(ctx, string(response.Kubeconfig), secretName, kubeClient)
	if err != nil {
		return nil, err
	}

	if created {
		fmt.Fprintf(b.opts.IOStreams.ErrOut, "🔒 Created secret %s/%s for host %s, namespace %s\n", "kube-bind", secret.Name, remoteHost, remoteNamespace)
	} else {
		fmt.Fprintf(b.opts.IOStreams.ErrOut, "🔒 Updated secret %s/%s for host %s, namespace %s\n", "kube-bind", secret.Name, remoteHost, remoteNamespace)
	}

	if b.opts.DryRun {
		return nil, nil
	}

	// Get remote kubeconfig
	remoteKubeconfig, remoteNamespaceActual, remoteConfig, err := b.getRemoteKubeconfig(ctx, secret.Namespace, secret.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote kubeconfig: %w", err)
	}

	// Extract the requests
	apiRequests := make([]*kubebindv1alpha2.APIServiceExportRequest, len(response.Requests))
	for i, request := range response.Requests {
		var meta metav1.TypeMeta
		if err := json.Unmarshal(request.Raw, &meta); err != nil {
			return nil, fmt.Errorf("unexpected response: failed to unmarshal request #%d: %v", i, err)
		}
		if got, expected := meta.APIVersion, kubebindv1alpha2.SchemeGroupVersion.String(); got != expected {
			return nil, fmt.Errorf("unexpected response: request #%d is not %s, got %s", i, expected, got)
		}
		var apiRequest kubebindv1alpha2.APIServiceExportRequest
		if err := json.Unmarshal(request.Raw, &apiRequest); err != nil {
			return nil, fmt.Errorf("failed to unmarshal api request #%d: %v", i+1, err)
		}
		apiRequests[i] = &apiRequest
	}

	// Deploy konnector if needed
	if err := b.deployKonnector(ctx); err != nil {
		return nil, fmt.Errorf("failed to deploy konnector: %w", err)
	}

	// Create bindings for all requests
	bindings := make([]*kubebindv1alpha2.APIServiceBinding, 0, len(apiRequests))
	for _, request := range apiRequests {
		result, err := b.createServiceExportRequest(ctx, remoteConfig, remoteNamespaceActual, request)
		if err != nil {
			return nil, fmt.Errorf("failed to create service export request: %w", err)
		}

		secretName, err := b.createKubeconfigSecret(ctx, remoteConfig.Host, remoteNamespaceActual, remoteKubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubeconfig secret: %w", err)
		}

		results, err := b.createAPIServiceBindings(ctx, result, secretName, response.ProviderID, true)
		if err != nil {
			return nil, fmt.Errorf("failed to create API service bindings: %w", err)
		}
		bindings = append(bindings, results...)
	}

	return bindings, nil
}

// Helper methods - these delegate to existing methods from BindAPIServiceOptions
func (b *Binder) ensureClientSideNamespaceExists(ctx context.Context) error {
	// Create a temporary BindAPIServiceOptions to reuse existing logic
	tempOpts := &BindAPIServiceOptions{
		Options: &base.Options{IOStreams: b.opts.IOStreams},
	}
	return tempOpts.ensureClientSideNamespaceExists(ctx, b.config)
}

func (b *Binder) getRemoteKubeconfig(ctx context.Context, namespace, name string) (kubeconfig, ns string, remoteConfig *rest.Config, err error) {
	tempOpts := &BindAPIServiceOptions{
		Options:                   &base.Options{IOStreams: b.opts.IOStreams},
		remoteKubeconfigFile:      b.opts.RemoteKubeconfigFile,
		remoteKubeconfigNamespace: b.opts.RemoteKubeconfigNamespace,
		remoteKubeconfigName:      b.opts.RemoteKubeconfigName,
	}
	return tempOpts.getRemoteKubeconfig(ctx, b.config, namespace, name)
}

func (b *Binder) deployKonnector(ctx context.Context) error {
	tempOpts := &BindAPIServiceOptions{
		Options:                  &base.Options{IOStreams: b.opts.IOStreams},
		SkipKonnector:            b.opts.SkipKonnector,
		KonnectorImageOverride:   b.opts.KonnectorImageOverride,
		DowngradeKonnector:       b.opts.DowngradeKonnector,
		DryRun:                   b.opts.DryRun,
		KonnectorHostAliasParsed: b.opts.KonnectorHostAliasParsed,
	}
	return tempOpts.DeployKonnector(ctx, b.config)
}

func (b *Binder) createServiceExportRequest(ctx context.Context, remoteConfig *rest.Config, remoteNamespace string, request *kubebindv1alpha2.APIServiceExportRequest) (*kubebindv1alpha2.APIServiceExportRequest, error) {
	tempOpts := &BindAPIServiceOptions{
		Options:         &base.Options{IOStreams: b.opts.IOStreams},
		remoteNamespace: b.opts.RemoteNamespace,
	}
	return tempOpts.createServiceExportRequest(ctx, remoteConfig, remoteNamespace, request)
}

func (b *Binder) createKubeconfigSecret(ctx context.Context, remoteHost, remoteNamespace, remoteKubeconfig string) (string, error) {
	tempOpts := &BindAPIServiceOptions{
		Options: &base.Options{IOStreams: b.opts.IOStreams},
	}

	return tempOpts.createKubeconfigSecret(ctx, b.config, remoteHost, remoteNamespace, remoteKubeconfig)
}

func (b *Binder) createAPIServiceBindings(ctx context.Context, request *kubebindv1alpha2.APIServiceExportRequest, secretName string, providerID string, isDefault bool) ([]*kubebindv1alpha2.APIServiceBinding, error) {
	tempOpts := &BindAPIServiceOptions{
		Options: &base.Options{IOStreams: b.opts.IOStreams},
	}
	return tempOpts.createAPIServiceBindings(ctx, b.config, request, secretName, providerID, isDefault)
}

func (b *Binder) getRequestManifest() ([]byte, error) {
	if b.opts.File == "-" {
		body, err := io.ReadAll(b.opts.IOStreams.In)
		if err != nil {
			return nil, fmt.Errorf("failed to read from stdin: %w", err)
		}
		return body, nil
	}

	body, err := os.ReadFile(b.opts.File)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", b.opts.File, err)
	}
	return body, nil
}

func (b *Binder) unmarshalManifest(bs []byte) (*kubebindv1alpha2.APIServiceExportRequest, error) {
	var request kubebindv1alpha2.APIServiceExportRequest
	if err := yaml.Unmarshal(bs, &request); err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}
	if request.APIVersion != kubebindv1alpha2.SchemeGroupVersion.String() {
		return nil, fmt.Errorf("invalid apiVersion %q", request.APIVersion)
	}
	if request.Kind != "APIServiceExportRequest" {
		return nil, fmt.Errorf("invalid kind %q", request.Kind)
	}
	return &request, nil
}
