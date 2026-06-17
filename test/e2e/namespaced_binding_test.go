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

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	"github.com/kbind/kbind/test/e2e/framework"
)

// TestSlimCoreNamespacedBinding exercises the namespaced Binding kind (the
// happy case uses ClusterBinding): it becomes Ready and pulls the CRD, and the
// syncer scopes instance sync to the Binding's namespace — instances elsewhere
// are not synced (ResolveConnection: a namespaced Binding covers only its own
// namespace).
func TestSlimCoreNamespacedBinding(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	gvr := env.InstallExportedWidgetCRD(t)

	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-provider"},
		Spec: corev1alpha1.ConnectionSpec{
			KubeconfigSecretRef: corev1alpha1.SecretKeyRef{Namespace: framework.KbindNamespace, Name: "demo-provider-kubeconfig", Key: "kubeconfig"},
			Schema:              corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
		},
	}))
	// A namespaced Binding scoped to "default".
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "widgets"},
		Spec: corev1alpha1.BindingSpec{
			ConnectionRef: corev1alpha1.ConnectionRef{Name: "demo-provider"},
			APIs:          []corev1alpha1.APIRef{{Name: widgetCRDName}},
		},
	}))

	t.Run("the namespaced Binding becomes Ready and pulls the CRD", func(t *testing.T) {
		framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
			b := &corev1alpha1.Binding{}
			err := env.ConsumerClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "widgets"}, b)
			return b.Status.Conditions, err
		}, corev1alpha1.ConditionReady)
		require.Eventually(t, func() bool {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			return env.ConsumerClient.Get(ctx, client.ObjectKey{Name: widgetCRDName}, crd) == nil
		}, 30*time.Second, 200*time.Millisecond, "the bound CRD should be pulled onto the consumer")
	})

	t.Run("an instance in the bound namespace syncs to the provider", func(t *testing.T) {
		_, err := env.ConsumerDyn.Resource(gvr).Namespace("default").Create(ctx, nsWidget("default", "in-scope"), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, err := env.ProviderDyn.Resource(gvr).Namespace("default").Get(ctx, "in-scope", metav1.GetOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "an instance in the bound namespace should sync to the provider")
	})

	t.Run("an instance in another namespace is not synced", func(t *testing.T) {
		require.NoError(t, env.ConsumerClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}}))
		_, err := env.ConsumerDyn.Resource(gvr).Namespace("other").Create(ctx, nsWidget("other", "out-of-scope"), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Never(t, func() bool {
			_, err := env.ProviderDyn.Resource(gvr).Namespace("other").Get(ctx, "out-of-scope", metav1.GetOptions{})
			return err == nil
		}, 4*time.Second, 300*time.Millisecond, "an instance outside the bound namespace must not sync")
	})
}

func nsWidget(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(framework.WidgetGVK())
	u.SetNamespace(ns)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, "small", "spec", "size")
	return u
}
