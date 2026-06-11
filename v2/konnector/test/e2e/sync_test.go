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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
	"github.com/kube-bind/kube-bind/v2/konnector/test/e2e/framework"
)

const (
	widgetCRDName = "widgets.example.org"
	instanceNS    = "default"
	instanceName  = "my-widget"
)

// TestSlimCoreHappyCase exercises the full v2 slim-core flow against two
// envtest API servers, mirroring the v1 happy-case step pattern: the one-apply
// bundle (Secret + Connection + ClusterBinding) drives CRD delivery and
// bidirectional instance sync, with conflict and deletion behavior verified.
func TestSlimCoreHappyCase(t *testing.T) {
	env := framework.Start(t)
	ctx := context.Background()

	gvr := env.InstallExportedWidgetCRD(t)
	consumerWidgets := env.ConsumerDyn.Resource(gvr).Namespace(instanceNS)
	providerWidgets := env.ProviderDyn.Resource(gvr).Namespace(instanceNS)

	// The "one apply" bundle: Connection + ClusterBinding on the consumer.
	// (The kubeconfig Secret was created by the framework.)
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-provider"},
		Spec: corev1alpha1.ConnectionSpec{
			KubeconfigSecretRef: corev1alpha1.SecretKeyRef{
				Namespace: framework.KubeBindNamespace,
				Name:      "demo-provider-kubeconfig",
				Key:       "kubeconfig",
			},
			Schema: corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
		},
	}))
	require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets"},
		Spec: corev1alpha1.BindingSpec{
			ConnectionRef: corev1alpha1.ConnectionRef{Name: "demo-provider"},
			APIs:          []corev1alpha1.APIRef{{Name: widgetCRDName}},
		},
	}))

	for _, tc := range []struct {
		name string
		step func(t *testing.T)
	}{
		{
			name: "Connection becomes Ready and discovers the exported API",
			step: func(t *testing.T) {
				framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
					conn := &corev1alpha1.Connection{}
					err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "demo-provider"}, conn)
					return conn.Status.Conditions, err
				}, corev1alpha1.ConditionReady)

				conn := &corev1alpha1.Connection{}
				require.NoError(t, env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "demo-provider"}, conn))
				require.NotEmpty(t, conn.Status.RemoteClusterUID, "remote cluster UID must be pinned")
				require.NotEmpty(t, conn.Status.LocalClusterUID, "local cluster UID must be pinned")
				_, ok := conn.Status.ExportsAPI(widgetCRDName)
				require.True(t, ok, "Connection must export %s", widgetCRDName)
			},
		},
		{
			name: "ClusterBinding becomes Ready and the CRD is pulled onto the consumer",
			step: func(t *testing.T) {
				framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
					cb := &corev1alpha1.ClusterBinding{}
					err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, cb)
					return cb.Status.Conditions, err
				}, corev1alpha1.ConditionReady)

				crd := &apiextensionsv1.CustomResourceDefinition{}
				require.Eventually(t, func() bool {
					return env.ConsumerClient.Get(ctx, client.ObjectKey{Name: widgetCRDName}, crd) == nil
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "Widget CRD should be pulled onto the consumer")
				require.Equal(t, "true", crd.GetLabels()[corev1alpha1.LabelManaged], "pulled CRD must be marked managed")
			},
		},
		{
			name: "instance spec syncs consumer -> provider with ownership markers and a finalizer",
			step: func(t *testing.T) {
				require.Eventually(t, func() bool {
					_, err := consumerWidgets.Create(ctx, widget(instanceName, "large"), metav1.CreateOptions{})
					return err == nil || apierrors.IsAlreadyExists(err)
				}, 30*time.Second, 200*time.Millisecond, "consumer Widget CRD should become creatable")

				var provObj *unstructured.Unstructured
				require.Eventually(t, func() bool {
					var err error
					provObj, err = providerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "Widget should sync to the provider")

				size, _, _ := unstructured.NestedString(provObj.Object, "spec", "size")
				require.Equal(t, "large", size)
				require.Equal(t, "true", provObj.GetLabels()[corev1alpha1.LabelManaged])
				require.NotEmpty(t, provObj.GetAnnotations()[corev1alpha1.AnnotationConsumerClusterUID], "consumer-cluster-uid marker")
				require.NotEmpty(t, provObj.GetAnnotations()[corev1alpha1.AnnotationConsumerObjectUID], "consumer-object-uid marker")

				require.Eventually(t, func() bool {
					obj, err := consumerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					if err != nil {
						return false
					}
					for _, f := range obj.GetFinalizers() {
						if f == corev1alpha1.FinalizerSyncer {
							return true
						}
					}
					return false
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer object should carry the syncer finalizer")
			},
		},
		{
			name: "status syncs provider -> consumer",
			step: func(t *testing.T) {
				require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
					obj, err := providerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					if err != nil {
						return err
					}
					_ = unstructured.SetNestedField(obj.Object, "Running", "status", "phase")
					_, err = providerWidgets.UpdateStatus(ctx, obj, metav1.UpdateOptions{})
					return err
				}))
				require.Eventually(t, func() bool {
					obj, err := consumerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					if err != nil {
						return false
					}
					phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
					return phase == "Running"
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "provider status should flow to the consumer")
			},
		},
		{
			name: "spec update syncs consumer -> provider",
			step: func(t *testing.T) {
				require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
					obj, err := consumerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					if err != nil {
						return err
					}
					_ = unstructured.SetNestedField(obj.Object, "xlarge", "spec", "size")
					_, err = consumerWidgets.Update(ctx, obj, metav1.UpdateOptions{})
					return err
				}))
				require.Eventually(t, func() bool {
					obj, err := providerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					if err != nil {
						return false
					}
					size, _, _ := unstructured.NestedString(obj.Object, "spec", "size")
					return size == "xlarge"
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer spec update should flow to the provider")
			},
		},
		{
			name: "conflict: a foreign provider object is not overwritten",
			step: func(t *testing.T) {
				// Pre-create a foreign object on the provider (no markers).
				_, err := providerWidgets.Create(ctx, widget("conflict-widget", "PROVIDER-OWNED"), metav1.CreateOptions{})
				require.NoError(t, err)
				// Create a same-named object on the consumer.
				_, err = consumerWidgets.Create(ctx, widget("conflict-widget", "CONSUMER-WANTS"), metav1.CreateOptions{})
				require.NoError(t, err)

				// The provider object must stay foreign and untouched.
				require.Never(t, func() bool {
					obj, err := providerWidgets.Get(ctx, "conflict-widget", metav1.GetOptions{})
					if err != nil {
						return false
					}
					size, _, _ := unstructured.NestedString(obj.Object, "spec", "size")
					return size != "PROVIDER-OWNED" || obj.GetLabels()[corev1alpha1.LabelManaged] == "true"
				}, 5*time.Second, 500*time.Millisecond, "foreign provider object must not be overwritten")
			},
		},
		{
			name: "deletion: consumer delete removes the provider copy and releases the finalizer",
			step: func(t *testing.T) {
				require.NoError(t, consumerWidgets.Delete(ctx, instanceName, metav1.DeleteOptions{}))
				require.Eventually(t, func() bool {
					_, err := providerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "provider copy should be deleted")
				require.Eventually(t, func() bool {
					_, err := consumerWidgets.Get(ctx, instanceName, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer object should finalize and disappear")
			},
		},
		{
			name: "deletion of a conflicting consumer object leaves the foreign provider object intact",
			step: func(t *testing.T) {
				require.NoError(t, consumerWidgets.Delete(ctx, "conflict-widget", metav1.DeleteOptions{}))
				require.Eventually(t, func() bool {
					_, err := consumerWidgets.Get(ctx, "conflict-widget", metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer conflict object should finalize")
				// Foreign provider object must survive.
				obj, err := providerWidgets.Get(ctx, "conflict-widget", metav1.GetOptions{})
				require.NoError(t, err, "foreign provider object must survive consumer deletion")
				size, _, _ := unstructured.NestedString(obj.Object, "spec", "size")
				require.Equal(t, "PROVIDER-OWNED", size)
			},
		},
	} {
		t.Run(tc.name, tc.step)
	}
}

func widget(name, size string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(framework.WidgetGVK())
	u.SetNamespace(instanceNS)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, size, "spec", "size")
	return u
}

func apiextensionsGVK() (gvk struct{}) { panic("unused") }
