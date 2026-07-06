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

// setupWidgetRelatedBinding creates the demo Connection (CRD schema source) and a
// Ready ClusterBinding for the Widget API carrying the given relatedResources
// rules. The Widget CRD must already be installed on the provider.
func setupWidgetRelatedBinding(t *testing.T, env *framework.Env, ctx context.Context, name string, rr []corev1alpha1.RelatedResource) {
	t.Helper()
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-provider"},
		Spec: corev1alpha1.ConnectionSpec{
			KubeconfigSecretRef: corev1alpha1.SecretKeyRef{Namespace: framework.KbindNamespace, Name: "demo-provider-kubeconfig", Key: "kubeconfig"},
			Schema:              corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
		},
	}))
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1alpha1.BindingSpec{
			ConnectionRef:    corev1alpha1.ConnectionRef{Name: "demo-provider"},
			APIs:             []corev1alpha1.APIRef{{Name: widgetCRDName}},
			RelatedResources: rr,
		},
	}))
	framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
		cb := &corev1alpha1.ClusterBinding{}
		err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: name}, cb)
		return cb.Status.Conditions, err
	}, corev1alpha1.ConditionReady)
}

// TestSlimCoreRelatedConfigMaps covers ConfigMaps as a related resource
// (FromProvider): sync, GC on stop-matching, and the foreign-object guard.
func TestSlimCoreRelatedConfigMaps(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	env.InstallExportedWidgetCRD(t)
	setupWidgetRelatedBinding(t, env, ctx, "widgets", []corev1alpha1.RelatedResource{{
		Resource:  "configmaps",
		Direction: corev1alpha1.FromProvider,
		Selector:  &corev1alpha1.RelatedResourceSelector{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widget"}}},
	}})

	key := client.ObjectKey{Namespace: "default", Name: "widget-config"}

	t.Run("a label-selected provider ConfigMap syncs to the consumer", func(t *testing.T) {
		require.NoError(t, env.ProviderClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "widget-config", Labels: map[string]string{"app": "widget"}},
			Data:       map[string]string{"region": "eu"},
		}))
		require.Eventually(t, func() bool {
			cm := &corev1.ConfigMap{}
			if err := env.ConsumerClient.Get(ctx, key, cm); err != nil {
				return false
			}
			return cm.Data["region"] == "eu" &&
				cm.Labels[corev1alpha1.LabelManaged] == "true" &&
				cm.Annotations[corev1alpha1.AnnotationRelatedBinding] != ""
		}, 30*time.Second, 200*time.Millisecond, "the label-selected provider ConfigMap should sync to the consumer")
	})

	t.Run("the synced ConfigMap is GC'd when it stops matching", func(t *testing.T) {
		cm := &corev1.ConfigMap{}
		require.NoError(t, env.ProviderClient.Get(ctx, key, cm))
		delete(cm.Labels, "app")
		require.NoError(t, env.ProviderClient.Update(ctx, cm))
		require.Eventually(t, func() bool {
			c := &corev1.ConfigMap{}
			return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, key, c))
		}, 30*time.Second, 200*time.Millisecond, "the consumer copy should be GC'd once the ConfigMap stops matching")
	})

	t.Run("a foreign ConfigMap of the same name is not overwritten", func(t *testing.T) {
		foreignKey := client.ObjectKey{Namespace: "default", Name: "foreign-config"}
		// A pre-existing, unmanaged consumer ConfigMap.
		require.NoError(t, env.ConsumerClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "foreign-config"},
			Data:       map[string]string{"region": "FOREIGN"},
		}))
		// The provider exports a same-named, matching ConfigMap.
		require.NoError(t, env.ProviderClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "foreign-config", Labels: map[string]string{"app": "widget"}},
			Data:       map[string]string{"region": "PROVIDER"},
		}))
		require.Never(t, func() bool {
			cm := &corev1.ConfigMap{}
			if err := env.ConsumerClient.Get(ctx, foreignKey, cm); err != nil {
				return false
			}
			return cm.Data["region"] == "PROVIDER" || cm.Labels[corev1alpha1.LabelManaged] == "true"
		}, 3*time.Second, 300*time.Millisecond, "the foreign consumer ConfigMap must not be overwritten or marked managed")
	})
}

// TestSlimCoreRelatedReverseDirection covers FromConsumer (consumer -> provider):
// a consumer Secret is synced UP to the provider and GC'd when it stops matching.
func TestSlimCoreRelatedReverseDirection(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	env.InstallExportedWidgetCRD(t)
	setupWidgetRelatedBinding(t, env, ctx, "widgets", []corev1alpha1.RelatedResource{{
		Resource:  "secrets",
		Direction: corev1alpha1.FromConsumer,
		Selector:  &corev1alpha1.RelatedResourceSelector{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widget"}}},
	}})

	key := client.ObjectKey{Namespace: "default", Name: "consumer-creds"}

	t.Run("a label-selected consumer Secret syncs up to the provider", func(t *testing.T) {
		require.NoError(t, env.ConsumerClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "consumer-creds", Labels: map[string]string{"app": "widget"}},
			StringData: map[string]string{"token": "up3cr3t"},
		}))
		require.Eventually(t, func() bool {
			s := &corev1.Secret{}
			if err := env.ProviderClient.Get(ctx, key, s); err != nil {
				return false
			}
			return string(s.Data["token"]) == "up3cr3t" &&
				s.Labels[corev1alpha1.LabelManaged] == "true" &&
				s.Annotations[corev1alpha1.AnnotationRelatedBinding] != ""
		}, 30*time.Second, 200*time.Millisecond, "the consumer Secret should sync up to the provider")
	})

	t.Run("the provider copy is GC'd when the consumer Secret stops matching", func(t *testing.T) {
		s := &corev1.Secret{}
		require.NoError(t, env.ConsumerClient.Get(ctx, key, s))
		delete(s.Labels, "app")
		require.NoError(t, env.ConsumerClient.Update(ctx, s))
		require.Eventually(t, func() bool {
			c := &corev1.Secret{}
			return apierrors.IsNotFound(env.ProviderClient.Get(ctx, key, c))
		}, 30*time.Second, 200*time.Millisecond, "the provider copy should be GC'd once the consumer Secret stops matching")
	})
}

// TestSlimCoreRelatedNamedSelector covers the named selector: only the listed
// objects are synced; others are left alone.
func TestSlimCoreRelatedNamedSelector(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	env.InstallExportedWidgetCRD(t)
	setupWidgetRelatedBinding(t, env, ctx, "widgets", []corev1alpha1.RelatedResource{{
		Resource:  "configmaps",
		Direction: corev1alpha1.FromProvider,
		Selector:  &corev1alpha1.RelatedResourceSelector{Names: []string{"wanted"}},
	}})

	require.NoError(t, env.ProviderClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "wanted"}, Data: map[string]string{"k": "v"},
	}))
	require.NoError(t, env.ProviderClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "unwanted"}, Data: map[string]string{"k": "v"},
	}))

	require.Eventually(t, func() bool {
		cm := &corev1.ConfigMap{}
		return env.ConsumerClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "wanted"}, cm) == nil
	}, 30*time.Second, 200*time.Millisecond, "the named ConfigMap should sync")

	// Once "wanted" has synced the reconcile loop has run, so a non-named object
	// would already have synced if names were ignored.
	require.Never(t, func() bool {
		cm := &corev1.ConfigMap{}
		return env.ConsumerClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "unwanted"}, cm) == nil
	}, 3*time.Second, 300*time.Millisecond, "the non-named ConfigMap must not sync")
}

// TestSlimCoreRelatedCleanupOnUnbind verifies that deleting the binding removes
// the related copies it created (cleanupRelated on the unbind finalizer).
func TestSlimCoreRelatedCleanupOnUnbind(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	env.InstallExportedWidgetCRD(t)
	setupWidgetRelatedBinding(t, env, ctx, "widgets", []corev1alpha1.RelatedResource{{
		Resource:  "secrets",
		Direction: corev1alpha1.FromProvider,
		Selector:  &corev1alpha1.RelatedResourceSelector{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widget"}}},
	}})

	key := client.ObjectKey{Namespace: "default", Name: "widget-creds"}
	require.NoError(t, env.ProviderClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "widget-creds", Labels: map[string]string{"app": "widget"}},
		StringData: map[string]string{"token": "s3cr3t"},
	}))
	require.Eventually(t, func() bool {
		s := &corev1.Secret{}
		return env.ConsumerClient.Get(ctx, key, s) == nil
	}, 30*time.Second, 200*time.Millisecond, "the related Secret should sync before unbind")

	require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1alpha1.ClusterBinding{ObjectMeta: metav1.ObjectMeta{Name: "widgets"}}))
	require.Eventually(t, func() bool {
		s := &corev1.Secret{}
		return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, key, s))
	}, 30*time.Second, 200*time.Millisecond, "the related copy should be cleaned up on unbind")
}
