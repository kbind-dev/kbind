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
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	"github.com/kbind/kbind/test/e2e/framework"
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
				Namespace: framework.KbindNamespace,
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
			name: "provider heartbeat Lease is maintained and renewed",
			step: func(t *testing.T) {
				leaseFor := func() (*coordinationv1.Lease, bool) {
					conn := &corev1alpha1.Connection{}
					if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "demo-provider"}, conn); err != nil || conn.Status.LocalClusterUID == "" {
						return nil, false
					}
					l := &coordinationv1.Lease{}
					key := client.ObjectKey{Namespace: framework.KbindNamespace, Name: "consumer-" + conn.Status.LocalClusterUID}
					if err := env.ProviderClient.Get(ctx, key, l); err != nil {
						return nil, false
					}
					return l, l.Spec.HolderIdentity != nil && *l.Spec.HolderIdentity == conn.Status.LocalClusterUID
				}

				var renew1 metav1.MicroTime
				require.Eventually(t, func() bool {
					l, ok := leaseFor()
					if !ok || l.Labels[corev1alpha1.LabelManaged] != "true" || l.Spec.RenewTime == nil {
						return false
					}
					renew1 = *l.Spec.RenewTime
					return true
				}, 30*time.Second, 200*time.Millisecond, "a heartbeat Lease should appear on the provider")

				require.Eventually(t, func() bool {
					l, ok := leaseFor()
					return ok && l.Spec.RenewTime != nil && l.Spec.RenewTime.After(renew1.Time)
				}, 30*time.Second, 200*time.Millisecond, "the heartbeat Lease should be renewed")
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

				// The consumer object is marked with the conflict annotation.
				require.Eventually(t, func() bool {
					o, err := consumerWidgets.Get(ctx, "conflict-widget", metav1.GetOptions{})
					return err == nil && o.GetAnnotations()[corev1alpha1.AnnotationConflict] != ""
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "consumer object should carry the conflict annotation")

				// The ClusterBinding surfaces conflictCount + the Conflicts condition.
				require.Eventually(t, func() bool {
					cb := &corev1alpha1.ClusterBinding{}
					if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, cb); err != nil {
						return false
					}
					var cnt int32
					for _, ba := range cb.Status.BoundAPIs {
						if ba.Name == widgetCRDName {
							cnt = ba.ConflictCount
						}
					}
					return cnt == 1 && isConditionTrue(cb.Status.Conditions, corev1alpha1.ConditionConflicts)
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "ClusterBinding should report conflictCount=1 and Conflicts=True")
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
		{
			// Re-discovery: a Connection already Ready picks up a CRD exported later.
			name: "re-discovery: a CRD exported after connect is picked up",
			step: func(t *testing.T) {
				env.InstallExportedCRD(t, "gadget.io", "gadgets", "gadget", "Gadget")
				require.Eventually(t, func() bool {
					conn := &corev1alpha1.Connection{}
					if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "demo-provider"}, conn); err != nil {
						return false
					}
					_, ok := conn.Status.ExportsAPI("gadgets.gadget.io")
					return ok
				}, 30*time.Second, 200*time.Millisecond, "Connection should re-discover the newly-exported gadgets API")
			},
		},
		{
			// conflictPolicy Adopt takes over an un-owned provider object.
			name: "conflictPolicy Adopt takes over an un-owned provider object",
			step: func(t *testing.T) {
				gadgetGVR := schema.GroupVersionResource{Group: "gadget.io", Version: "v1", Resource: "gadgets"}
				gadgetGVK := schema.GroupVersionKind{Group: "gadget.io", Version: "v1", Kind: "Gadget"}
				consumerGadgets := env.ConsumerDyn.Resource(gadgetGVR).Namespace(instanceNS)
				providerGadgets := env.ProviderDyn.Resource(gadgetGVR).Namespace(instanceNS)
				gadget := func(name, size string) *unstructured.Unstructured {
					u := &unstructured.Unstructured{}
					u.SetGroupVersionKind(gadgetGVK)
					u.SetNamespace(instanceNS)
					u.SetName(name)
					_ = unstructured.SetNestedField(u.Object, size, "spec", "size")
					return u
				}

				require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "gadgets"},
					Spec: corev1alpha1.BindingSpec{
						ConnectionRef:  corev1alpha1.ConnectionRef{Name: "demo-provider"},
						APIs:           []corev1alpha1.APIRef{{Name: "gadgets.gadget.io"}},
						ConflictPolicy: corev1alpha1.ConflictPolicyAdopt,
					},
				}))
				framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
					cb := &corev1alpha1.ClusterBinding{}
					err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "gadgets"}, cb)
					return cb.Status.Conditions, err
				}, corev1alpha1.ConditionReady)

				// Pre-create a FOREIGN gadget on the provider (no markers).
				_, err := providerGadgets.Create(ctx, gadget("shared-gadget", "PROVIDER-OWNED"), metav1.CreateOptions{})
				require.NoError(t, err)
				// Create the same-named gadget on the consumer (CRD now pulled).
				require.Eventually(t, func() bool {
					_, err := consumerGadgets.Create(ctx, gadget("shared-gadget", "ADOPTED"), metav1.CreateOptions{})
					return err == nil || apierrors.IsAlreadyExists(err)
				}, 30*time.Second, 200*time.Millisecond, "consumer Gadget CRD should become creatable")

				// Adopt: the provider object gets our markers and its spec is
				// overwritten to the consumer's value.
				require.Eventually(t, func() bool {
					o, err := providerGadgets.Get(ctx, "shared-gadget", metav1.GetOptions{})
					if err != nil {
						return false
					}
					size, _, _ := unstructured.NestedString(o.Object, "spec", "size")
					return size == "ADOPTED" &&
						o.GetLabels()[corev1alpha1.LabelManaged] == "true" &&
						o.GetAnnotations()[corev1alpha1.AnnotationConsumerObjectUID] != ""
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "Adopt should take over the un-owned provider object")
			},
		},
		{
			// Gap 1: a Connection created before its Secret must resolve once the
			// Secret arrives (order-independent "one apply").
			name: "Connection created before its Secret resolves when the Secret arrives",
			step: func(t *testing.T) {
				require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
					ObjectMeta: metav1.ObjectMeta{Name: "late-provider"},
					Spec: corev1alpha1.ConnectionSpec{
						KubeconfigSecretRef: corev1alpha1.SecretKeyRef{
							Namespace: framework.KbindNamespace,
							Name:      "late-secret",
							Key:       "kubeconfig",
						},
						Schema: corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
					},
				}))
				// It must be not-Ready while the Secret is missing.
				require.Never(t, func() bool {
					conn := &corev1alpha1.Connection{}
					if err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "late-provider"}, conn); err != nil {
						return false
					}
					return isConditionTrue(conn.Status.Conditions, corev1alpha1.ConditionReady)
				}, 3*time.Second, 500*time.Millisecond, "must not be Ready without its Secret")

				// Now create the Secret (copy of the working one).
				require.NoError(t, env.ConsumerClient.Create(ctx, env.CopyProviderSecret(t, "late-secret")))
				framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
					conn := &corev1alpha1.Connection{}
					err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "late-provider"}, conn)
					return conn.Status.Conditions, err
				}, corev1alpha1.ConditionReady)
			},
		},
		{
			// The Secret cannot be hard-deleted while its Connection exists (it
			// carries a cleanup finalizer); updates stay allowed. It is released
			// only when the Connection is deleted.
			name: "Secret survives deletion while its Connection exists, released on Connection delete",
			step: func(t *testing.T) {
				secretKey := client.ObjectKey{Namespace: framework.KbindNamespace, Name: "late-secret"}
				// The Connection reconcile should have stamped the finalizer.
				require.Eventually(t, func() bool {
					s := &corev1.Secret{}
					if err := env.ConsumerClient.Get(ctx, secretKey, s); err != nil {
						return false
					}
					for _, f := range s.Finalizers {
						if f == corev1alpha1.FinalizerCleanup {
							return true
						}
					}
					return false
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "Secret should carry the cleanup finalizer")

				// Deleting the Secret must NOT remove it while the Connection lives.
				require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: secretKey.Namespace, Name: secretKey.Name},
				}))
				require.Never(t, func() bool {
					s := &corev1.Secret{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, secretKey, s))
				}, 3*time.Second, 500*time.Millisecond, "Secret must persist (Terminating) while its Connection exists")

				// Deleting the Connection releases the Secret.
				require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1alpha1.Connection{
					ObjectMeta: metav1.ObjectMeta{Name: "late-provider"},
				}))
				require.Eventually(t, func() bool {
					s := &corev1.Secret{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, secretKey, s))
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "Secret should be released once the Connection is deleted")
				require.Eventually(t, func() bool {
					c := &corev1alpha1.Connection{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "late-provider"}, c))
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "Connection should finalize")
			},
		},
		{
			// A Secret shared by multiple Connections keeps its finalizer until
			// the LAST Connection referencing it is gone.
			name: "Secret shared by multiple Connections is released only when the last one is deleted",
			step: func(t *testing.T) {
				sharedKey := client.ObjectKey{Namespace: framework.KbindNamespace, Name: "shared-secret"}
				require.NoError(t, env.ConsumerClient.Create(ctx, env.CopyProviderSecret(t, "shared-secret")))
				for _, name := range []string{"shared-a", "shared-b"} {
					require.NoError(t, env.ConsumerClient.Create(ctx, &corev1alpha1.Connection{
						ObjectMeta: metav1.ObjectMeta{Name: name},
						Spec: corev1alpha1.ConnectionSpec{
							KubeconfigSecretRef: corev1alpha1.SecretKeyRef{
								Namespace: framework.KbindNamespace, Name: "shared-secret", Key: "kubeconfig",
							},
							Schema: corev1alpha1.SchemaPolicy{Source: corev1alpha1.SchemaSourceCRD},
						},
					}))
				}
				for _, name := range []string{"shared-a", "shared-b"} {
					framework.WaitForConditionTrue(t, func() ([]metav1.Condition, error) {
						c := &corev1alpha1.Connection{}
						err := env.ConsumerClient.Get(ctx, client.ObjectKey{Name: name}, c)
						return c.Status.Conditions, err
					}, corev1alpha1.ConditionReady)
				}

				// Request deletion of the shared Secret — it must stay (finalizer).
				require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: sharedKey.Namespace, Name: sharedKey.Name},
				}))

				// Delete the first Connection — the Secret must survive (the second
				// Connection still holds it).
				require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1alpha1.Connection{
					ObjectMeta: metav1.ObjectMeta{Name: "shared-a"},
				}))
				require.Eventually(t, func() bool {
					c := &corev1alpha1.Connection{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "shared-a"}, c))
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "shared-a should finalize")
				require.Never(t, func() bool {
					s := &corev1.Secret{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, sharedKey, s))
				}, 3*time.Second, 500*time.Millisecond, "shared Secret must survive while shared-b still references it")

				// Delete the second (last) Connection — now the Secret is released
				// and its pending deletion proceeds.
				require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1alpha1.Connection{
					ObjectMeta: metav1.ObjectMeta{Name: "shared-b"},
				}))
				require.Eventually(t, func() bool {
					s := &corev1.Secret{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, sharedKey, s))
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "shared Secret should be released after the last Connection is deleted")
			},
		},
		{
			// Gap 2: deleting the ClusterBinding unbinds — provider copies gone,
			// pulled CRD removed, consumer instances cascade away, finalizer freed.
			name: "deleting the ClusterBinding unbinds and cleans up",
			step: func(t *testing.T) {
				// Seed a fresh synced instance.
				_, err := consumerWidgets.Create(ctx, widget("unbind-widget", "small"), metav1.CreateOptions{})
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					_, err := providerWidgets.Get(ctx, "unbind-widget", metav1.GetOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "unbind-widget should sync to the provider")

				// Unbind.
				require.NoError(t, env.ConsumerClient.Delete(ctx, &corev1alpha1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "widgets"},
				}))

				// ClusterBinding finalizes and disappears.
				require.Eventually(t, func() bool {
					cb := &corev1alpha1.ClusterBinding{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, client.ObjectKey{Name: "widgets"}, cb))
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "ClusterBinding should finalize")

				// Provider copy deleted.
				require.Eventually(t, func() bool {
					_, err := providerWidgets.Get(ctx, "unbind-widget", metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "provider copy should be cleaned up on unbind")

				// Pulled CRD removed → consumer instances cascade away.
				require.Eventually(t, func() bool {
					crd := &apiextensionsv1.CustomResourceDefinition{}
					return apierrors.IsNotFound(env.ConsumerClient.Get(ctx, client.ObjectKey{Name: widgetCRDName}, crd))
				}, wait.ForeverTestTimeout, 200*time.Millisecond, "pulled CRD should be removed on unbind")
			},
		},
	} {
		t.Run(tc.name, tc.step)
	}
}

func isConditionTrue(conds []metav1.Condition, t string) bool {
	for _, c := range conds {
		if c.Type == t {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

func widget(name, size string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(framework.WidgetGVK())
	u.SetNamespace(instanceNS)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, size, "spec", "size")
	return u
}
