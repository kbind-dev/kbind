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

// Package remote resolves provider credentials and cluster identity for a
// Connection.
package remote

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// ProviderClient builds a direct (uncached) client for the provider cluster a
// Connection points at. Used for one-shot reads/writes (discovery, CRD pull)
// that do not warrant a cache.
func ProviderClient(ctx context.Context, local client.Client, conn *corev1alpha1.Connection, scheme *runtime.Scheme) (client.Client, error) {
	cfg, err := RestConfigFromConnection(ctx, local, conn)
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

// RestConfigFromConnection reads the Connection's kubeconfig Secret and returns
// a rest.Config for the provider cluster.
func RestConfigFromConnection(ctx context.Context, c client.Client, conn *corev1alpha1.Connection) (*rest.Config, error) {
	ref := conn.Spec.KubeconfigSecretRef
	key := ref.Key
	if key == "" {
		key = "kubeconfig"
	}

	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &secret); err != nil {
		return nil, fmt.Errorf("getting kubeconfig secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	data, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("kubeconfig secret %s/%s has no key %q", ref.Namespace, ref.Name, key)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("kubeconfig secret %s/%s key %q is empty", ref.Namespace, ref.Name, key)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	return cfg, nil
}

// ClusterUID returns a stable identity for the cluster behind c, derived from
// the kube-system namespace UID. This works on plain Kubernetes providers.
//
// TODO(v2): kcp / logical-cluster providers have no kube-system namespace; the
// OpenAPI path will need a different identity source (proposal gap #7).
func ClusterUID(ctx context.Context, c client.Client) (string, error) {
	var ns corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: "kube-system"}, &ns); err != nil {
		return "", fmt.Errorf("getting kube-system namespace for cluster identity: %w", err)
	}
	if ns.UID == "" {
		return "", fmt.Errorf("kube-system namespace UID is empty")
	}
	return string(ns.UID), nil
}
