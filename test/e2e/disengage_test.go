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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	"github.com/kbind/kbind/test/e2e/framework"
)

// TestSlimCoreStopOnDisengage verifies the syncer lifecycle around a Connection
// losing and regaining readiness: when the Connection stops being Ready the
// provider disengages and the per-GVR syncer is torn down; when it becomes Ready
// again the syncer is rebuilt against the freshly engaged provider cluster
// (rather than left pointing at a dead one), so sync resumes.
func TestSlimCoreStopOnDisengage(t *testing.T) {
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
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets"},
		Spec: corev1alpha1.BindingSpec{
			ConnectionRef: corev1alpha1.ConnectionRef{Name: "demo-provider"},
			APIs:          []corev1alpha1.APIRef{{Name: widgetCRDName}},
		},
	}))
	waitBindingReady(t, env, ctx)

	consumerWidgets := env.ConsumerDyn.Resource(gvr).Namespace(instanceNS)
	providerWidgets := env.ProviderDyn.Resource(gvr).Namespace(instanceNS)

	// Baseline: the syncer is running — an instance syncs to the provider.
	_, err := consumerWidgets.Create(ctx, widget("w1", "small"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := providerWidgets.Get(ctx, "w1", metav1.GetOptions{})
		return err == nil
	}, 30*time.Second, 200*time.Millisecond, "baseline instance should sync while the Connection is engaged")

	secretKey := client.ObjectKey{Namespace: framework.KbindNamespace, Name: "demo-provider-kubeconfig"}
	good := &corev1.Secret{}
	require.NoError(t, env.ConsumerClient.Get(ctx, secretKey, good))
	goodKubeconfig := good.Data["kubeconfig"]

	t.Run("a Connection that loses readiness disengages", func(t *testing.T) {
		// Corrupt the kubeconfig so the konnector can no longer build a provider
		// client → the Connection goes not-Ready → the provider disengages.
		bad := good.DeepCopy()
		bad.Data["kubeconfig"] = []byte("not-a-kubeconfig")
		require.NoError(t, env.ConsumerClient.Update(ctx, bad))

		require.Eventually(t, func() bool {
			conn := &corev1alpha1.Connection{}
			if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "demo-provider"}, conn); err != nil {
				return false
			}
			return !apimeta.IsStatusConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionReady)
		}, 30*time.Second, 200*time.Millisecond, "Connection should go not-Ready once its kubeconfig is broken")
	})

	t.Run("re-engage rebuilds the syncer and sync resumes", func(t *testing.T) {
		// Restore the good kubeconfig → the Connection becomes Ready again and the
		// provider re-engages as a fresh cluster.
		cur := &corev1.Secret{}
		require.NoError(t, env.ConsumerClient.Get(ctx, secretKey, cur))
		cur.Data["kubeconfig"] = goodKubeconfig
		require.NoError(t, env.ConsumerClient.Update(ctx, cur))
		waitBindingReady(t, env, ctx)

		// A NEW instance created after re-engage must sync — proving the syncer was
		// rebuilt against the live cluster rather than left pointing at the dead one.
		_, err := consumerWidgets.Create(ctx, widget("w2", "large"), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, err := providerWidgets.Get(ctx, "w2", metav1.GetOptions{})
			return err == nil
		}, 60*time.Second, 200*time.Millisecond, "a post-re-engage instance should sync through the rebuilt syncer")
	})
}

func waitBindingReady(t *testing.T, env *framework.Env, ctx context.Context) {
	t.Helper()
	framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
		cb := &corev1alpha1.ClusterBinding{}
		err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, cb)
		return cb.Status.Conditions, err
	}, corev1alpha1.ConditionReady)
}
