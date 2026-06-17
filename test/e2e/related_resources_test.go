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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	"github.com/kbind/kbind/test/e2e/framework"
)

// TestSlimCoreRelatedResources binds the Widget API with a relatedResources rule
// that syncs label-selected Secrets FromProvider, and verifies sync + GC.
func TestSlimCoreRelatedResources(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	env.InstallExportedWidgetCRD(t)

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
			RelatedResources: []corev1alpha1.RelatedResource{{
				Group:     "",
				Resource:  "secrets",
				Direction: corev1alpha1.FromProvider,
				Selector: &corev1alpha1.RelatedResourceSelector{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widget"}},
				},
			}},
		},
	}))
	framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
		cb := &corev1alpha1.ClusterBinding{}
		err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, cb)
		return cb.Status.Conditions, err
	}, corev1alpha1.ConditionReady)

	secretKey := client.ObjectKey{Namespace: "default", Name: "widget-creds"}

	t.Run("a label-selected provider Secret syncs to the consumer", func(t *testing.T) {
		require.NoError(t, env.ProviderClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "widget-creds", Labels: map[string]string{"app": "widget"}},
			StringData: map[string]string{"token": "s3cr3t"},
		}))
		require.Eventually(t, func() bool {
			s := &corev1.Secret{}
			if err := env.ConsumerClient.Get(ctx, secretKey, s); err != nil {
				return false
			}
			return string(s.Data["token"]) == "s3cr3t" &&
				s.Labels[corev1alpha1.LabelManaged] == "true" &&
				s.Annotations[corev1alpha1.AnnotationRelatedBinding] != ""
		}, 30*time.Second, 200*time.Millisecond, "the label-selected provider Secret should sync to the consumer")
	})

	t.Run("the synced copy is GC'd when it stops matching", func(t *testing.T) {
		s := &corev1.Secret{}
		require.NoError(t, env.ProviderClient.Get(ctx, secretKey, s))
		delete(s.Labels, "app")
		require.NoError(t, env.ProviderClient.Update(ctx, s))

		require.Eventually(t, func() bool {
			c := &corev1.Secret{}
			return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, secretKey, c))
		}, 30*time.Second, 200*time.Millisecond, "the consumer copy should be GC'd once the Secret stops matching the selector")
	})
}
