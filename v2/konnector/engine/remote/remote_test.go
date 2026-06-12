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

package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func identityScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	// Register the kcp LogicalCluster GVK as unstructured so the fake client can
	// serve it.
	s.AddKnownTypeWithName(logicalClusterGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(logicalClusterGVK.GroupVersion().WithKind("LogicalClusterList"), &unstructured.UnstructuredList{})
	return s
}

func logicalCluster(uid string) *unstructured.Unstructured {
	lc := &unstructured.Unstructured{}
	lc.SetGroupVersionKind(logicalClusterGVK)
	lc.SetName("cluster")
	lc.SetUID(types.UID(uid))
	return lc
}

func TestClusterUID_KubeSystem(t *testing.T) {
	s := identityScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "ks-uid"}},
	).Build()

	uid, err := ClusterUID(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, "ks-uid", uid, "plain Kubernetes should use the kube-system namespace UID")
}

func TestClusterUID_KCPLogicalClusterFallback(t *testing.T) {
	s := identityScheme(t)
	// No kube-system namespace (a kcp logical cluster) — only a LogicalCluster.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(logicalCluster("lc-uid")).Build()

	uid, err := ClusterUID(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, "lc-uid", uid, "a kcp logical cluster should fall back to the LogicalCluster UID")
}

func TestClusterUID_LogicalClusterWinsWhenBothPresent(t *testing.T) {
	s := identityScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "ks-uid"}},
		logicalCluster("lc-uid"),
	).Build()

	uid, err := ClusterUID(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, "lc-uid", uid, "a kcp-shaped cluster (LogicalCluster present) should prefer the LogicalCluster UID")
}

func TestClusterUID_NoSource(t *testing.T) {
	s := identityScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	_, err := ClusterUID(context.Background(), c)
	require.Error(t, err, "with neither source the identity is undeterminable")
}
