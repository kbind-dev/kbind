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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kube-bind/kube-bind/v2/konnector/test/e2e/framework"
	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

// TestSlimCoreTier2 exercises the Tier-2 "Decided" features. It shares one
// envtest pair (spinning a fresh one per case exhausts envtest resources):
// deletion-policy Orphan, updatePolicy Always, autoBind, pullPolicy All, and
// PermissionDenied. The widgets-dependent cases run first, before the extra
// Connections (autoBind/All) re-stamp the widgets CRD.
func TestSlimCoreTier2(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()
	gvr := env.InstallExportedWidgetCRD(t)
	consumerWidgets := env.ConsumerDyn.Resource(gvr).Namespace(instanceNS)
	providerWidgets := env.ProviderDyn.Resource(gvr).Namespace(instanceNS)

	// Base bundle: demo-provider Connection + widgets ClusterBinding.
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-provider"},
		Spec: corev1alpha1.ConnectionSpec{
			KubeconfigSecretRef: corev1alpha1.SecretKeyRef{Namespace: framework.KubeBindNamespace, Name: "demo-provider-kubeconfig", Key: "kubeconfig"},
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
	framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
		cb := &corev1alpha1.ClusterBinding{}
		err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, cb)
		return cb.Status.Conditions, err
	}, corev1alpha1.ConditionReady)

	t.Run("deletion-policy Orphan keeps the provider copy", func(t *testing.T) {
		w := widget("orphan-widget", "keep")
		w.SetAnnotations(map[string]string{corev1alpha1.AnnotationDeletionPolicy: corev1alpha1.DeletionPolicyOrphan})
		_, err := consumerWidgets.Create(ctx, w, metav1.CreateOptions{})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, err := providerWidgets.Get(ctx, "orphan-widget", metav1.GetOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "orphan-widget should sync to the provider")

		require.NoError(t, consumerWidgets.Delete(ctx, "orphan-widget", metav1.DeleteOptions{}))
		require.Eventually(t, func() bool {
			_, err := consumerWidgets.Get(ctx, "orphan-widget", metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer object should finalize despite Orphan")
		require.Never(t, func() bool {
			_, err := providerWidgets.Get(ctx, "orphan-widget", metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 3*time.Second, 500*time.Millisecond, "Orphan must keep the provider copy")
	})

	t.Run("updatePolicy Always follows provider CRD changes", func(t *testing.T) {
		require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := env.ProviderClient.Get(ctx, client.ObjectKey{Name: widgetCRDName}, crd); err != nil {
				return err
			}
			crd.Spec.Versions[0].AdditionalPrinterColumns = append(crd.Spec.Versions[0].AdditionalPrinterColumns,
				apiextensionsv1.CustomResourceColumnDefinition{Name: "colour", Type: "string", JSONPath: ".spec.colour"})
			return env.ProviderClient.Update(ctx, crd)
		}))
		require.Eventually(t, func() bool {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: widgetCRDName}, crd); err != nil {
				return false
			}
			for _, v := range crd.Spec.Versions {
				for _, col := range v.AdditionalPrinterColumns {
					if col.Name == "colour" {
						return true
					}
				}
			}
			return false
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer CRD should follow the provider schema change (updatePolicy: Always)")
	})

	t.Run("autoBind maintains a managed ClusterBinding", func(t *testing.T) {
		require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "auto-provider"},
			Spec: corev1alpha1.ConnectionSpec{
				KubeconfigSecretRef: corev1alpha1.SecretKeyRef{Namespace: framework.KubeBindNamespace, Name: "demo-provider-kubeconfig", Key: "kubeconfig"},
				Schema:              corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
				AutoBind:            true,
			},
		}))
		require.Eventually(t, func() bool {
			cb := &corev1alpha1.ClusterBinding{}
			if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "auto-provider"}, cb); err != nil {
				return false
			}
			if cb.Labels[corev1alpha1.LabelManaged] != "true" || len(cb.OwnerReferences) == 0 {
				return false
			}
			for _, a := range cb.Spec.APIs {
				if a.Name == widgetCRDName {
					return true
				}
			}
			return false
		}, 30*time.Second, 200*time.Millisecond, "autoBind should maintain a managed ClusterBinding mirroring exportedAPIs")
	})

	t.Run("pullPolicy All installs CRDs without a binding", func(t *testing.T) {
		env.InstallExportedCRD(t, "doodad.io", "doodads", "doodad", "Doodad")
		require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "all-provider"},
			Spec: corev1alpha1.ConnectionSpec{
				KubeconfigSecretRef: corev1alpha1.SecretKeyRef{Namespace: framework.KubeBindNamespace, Name: "demo-provider-kubeconfig", Key: "kubeconfig"},
				Schema:              corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD, PullPolicy: corev1alpha1.PullPolicyAll},
			},
		}))
		require.Eventually(t, func() bool {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "doodads.doodad.io"}, crd)
			return err == nil && crd.Labels[corev1alpha1.LabelManaged] == "true"
		}, 30*time.Second, 200*time.Millisecond, "pullPolicy: All should install the CRD without a binding")
	})

	t.Run("PermissionDenied surfaces on a restricted Connection", func(t *testing.T) {
		require.NoError(t, env.ConsumerClient.Create(ctx, env.RestrictedProviderSecret(t, "restricted-secret")))
		require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "restricted-provider"},
			Spec: corev1alpha1.ConnectionSpec{
				KubeconfigSecretRef: corev1alpha1.SecretKeyRef{Namespace: framework.KubeBindNamespace, Name: "restricted-secret", Key: "kubeconfig"},
				Schema:              corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
			},
		}))
		require.Eventually(t, func() bool {
			conn := &corev1alpha1.Connection{}
			if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "restricted-provider"}, conn); err != nil {
				return false
			}
			return isConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionPermissionDenied) &&
				!isConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionReady)
		}, 30*time.Second, 200*time.Millisecond, "a restricted Connection should report PermissionDenied and not be Ready")
	})
}
