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

package crdpull

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/kube-bind/kube-bind/v2/sdk/apis/core/v1alpha1"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	return s
}

func providerCRD(printerCol string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.org"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.org",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets", Kind: "Widget"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    "v1",
				Served:  true,
				Storage: true,
				AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
					{Name: printerCol, Type: "string", JSONPath: ".spec." + printerCol},
				},
			}},
		},
	}
}

func TestPull_BoundCreatesCRD(t *testing.T) {
	s := scheme(t)
	provider := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("size")).Build()
	consumer := fake.NewClientBuilder().WithScheme(s).Build()

	hash, installed, err := Pull(context.Background(), consumer, provider, "widgets.example.org", "conn", Options{Create: true})
	require.NoError(t, err)
	require.True(t, installed)
	require.NotEmpty(t, hash)

	got := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, consumer.Get(context.Background(), client.ObjectKey{Name: "widgets.example.org"}, got))
	require.Equal(t, "true", got.Labels[corev1alpha1.LabelManaged])
	require.Equal(t, "conn", got.Annotations[corev1alpha1.AnnotationConnection])
}

func TestPull_UpdatePolicyOnce_DoesNotUpdate(t *testing.T) {
	s := scheme(t)
	provider := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("size")).Build()
	consumer := fake.NewClientBuilder().WithScheme(s).Build()

	// First pull installs it.
	_, _, err := Pull(context.Background(), consumer, provider, "widgets.example.org", "conn", Options{Create: true, Update: true})
	require.NoError(t, err)

	// Provider schema changes, but updatePolicy: Once (Update=false) pins it.
	provider2 := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("colour")).Build()
	_, _, err = Pull(context.Background(), consumer, provider2, "widgets.example.org", "conn", Options{Create: true, Update: false})
	require.NoError(t, err)

	got := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, consumer.Get(context.Background(), client.ObjectKey{Name: "widgets.example.org"}, got))
	require.Equal(t, "size", got.Spec.Versions[0].AdditionalPrinterColumns[0].Name, "Once must not follow provider changes")
}

func TestPull_UpdatePolicyAlways_FollowsProviderChanges(t *testing.T) {
	s := scheme(t)
	provider := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("size")).Build()
	consumer := fake.NewClientBuilder().WithScheme(s).Build()

	_, hash1, err := pull3(t, consumer, provider, Options{Create: true, Update: true})
	require.NoError(t, err)

	provider2 := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("colour")).Build()
	_, hash2, err := pull3(t, consumer, provider2, Options{Create: true, Update: true})
	require.NoError(t, err)
	require.NotEqual(t, hash1, hash2, "schema hash must change when provider schema changes")

	got := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, consumer.Get(context.Background(), client.ObjectKey{Name: "widgets.example.org"}, got))
	require.Equal(t, "colour", got.Spec.Versions[0].AdditionalPrinterColumns[0].Name, "Always must follow provider changes")
}

func TestPull_NoneAbsent_NotInstalled(t *testing.T) {
	s := scheme(t)
	provider := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("size")).Build()
	consumer := fake.NewClientBuilder().WithScheme(s).Build()

	_, installed, err := Pull(context.Background(), consumer, provider, "widgets.example.org", "conn", Options{Create: false})
	require.NoError(t, err)
	require.False(t, installed, "pullPolicy None must not create the CRD")

	got := &apiextensionsv1.CustomResourceDefinition{}
	require.True(t, client.IgnoreNotFound(consumer.Get(context.Background(), client.ObjectKey{Name: "widgets.example.org"}, got)) == nil)
	require.Empty(t, got.Name)
}

func TestPull_NonePresent_StampsMarkers(t *testing.T) {
	s := scheme(t)
	// A user-installed CRD already on the consumer, without our markers.
	existing := providerCRD("size")
	provider := fake.NewClientBuilder().WithScheme(s).WithObjects(providerCRD("size")).Build()
	consumer := fake.NewClientBuilder().WithScheme(s).WithObjects(existing.DeepCopy()).Build()

	_, installed, err := Pull(context.Background(), consumer, provider, "widgets.example.org", "conn", Options{Create: false})
	require.NoError(t, err)
	require.True(t, installed)

	got := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, consumer.Get(context.Background(), client.ObjectKey{Name: "widgets.example.org"}, got))
	require.Equal(t, "true", got.Labels[corev1alpha1.LabelManaged], "None must stamp an existing CRD so the syncer finds it")
	require.Equal(t, "conn", got.Annotations[corev1alpha1.AnnotationConnection])
}

// pull3 returns (installed, hash, err) reordered for convenience.
func pull3(t *testing.T, consumer client.Client, provider client.Reader, opts Options) (bool, string, error) {
	t.Helper()
	hash, installed, err := Pull(context.Background(), consumer, provider, "widgets.example.org", "conn", opts)
	return installed, hash, err
}
