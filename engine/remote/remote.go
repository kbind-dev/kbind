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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
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

// logicalClusterGVK is kcp's per-workspace singleton identity object.
var logicalClusterGVK = schema.GroupVersionKind{Group: "core.kcp.io", Version: "v1alpha1", Kind: "LogicalCluster"}

// ClusterUID returns a stable identity for the cluster behind c. It tries, in
// order:
//   - the kcp LogicalCluster "cluster" object's UID (kcp / logical clusters,
//     which have no kube-system namespace) — preferred when present, since only
//     a kcp-shaped cluster serves core.kcp.io; then
//   - the kube-system namespace UID (plain Kubernetes).
//
// A Forbidden error from a source is surfaced (so the caller can report
// PermissionDenied) rather than silently falling through.
func ClusterUID(ctx context.Context, c client.Client) (string, error) {
	lc := &unstructured.Unstructured{}
	lc.SetGroupVersionKind(logicalClusterGVK)
	lcErr := c.Get(ctx, types.NamespacedName{Name: "cluster"}, lc)
	if lcErr == nil && lc.GetUID() != "" {
		return string(lc.GetUID()), nil
	}
	if apierrors.IsForbidden(lcErr) {
		return "", lcErr
	}

	var ns corev1.Namespace
	nsErr := c.Get(ctx, types.NamespacedName{Name: "kube-system"}, &ns)
	if nsErr == nil && ns.UID != "" {
		return string(ns.UID), nil
	}
	if apierrors.IsForbidden(nsErr) {
		return "", nsErr
	}

	return "", fmt.Errorf("cannot determine cluster identity (no LogicalCluster: %v; no kube-system namespace: %v)", lcErr, nsErr)
}
