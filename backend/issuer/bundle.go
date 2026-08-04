/*
Copyright 2026 The Kbind Authors.

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

package issuer

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

// KonnectorNamespace is the consumer-side namespace the konnector runs in and
// where the bundle's kubeconfig Secret lands.
const KonnectorNamespace = "kbind"

// BundleBuilder assembles the one-apply bundle for a Ready Grant. The bundle
// is never stored: it is (re)built on demand from the issuer's token Secret.
type BundleBuilder struct {
	// Client is a provider-cluster client (reads the token Secret).
	Client client.Client
	// ExternalAddress is the provider API server URL consumers reach —
	// what goes into issued kubeconfigs.
	ExternalAddress string
	// ExternalCA optionally overrides the CA bundle for issued kubeconfigs;
	// nil uses the ca.crt from the SA token Secret.
	ExternalCA []byte
}

// Kubeconfig builds the consumer kubeconfig for a provisioned Grant. The
// context namespace is pinned to the boundary namespace — tenant RBAC allows
// Leases there, and the konnector homes its heartbeat in the kubeconfig's
// context namespace.
func (b *BundleBuilder) Kubeconfig(ctx context.Context, grant *iamv1alpha1.Grant) ([]byte, error) {
	if grant.Status.Namespace == "" || grant.Status.TokenSecret == "" {
		return nil, fmt.Errorf("grant %s is not provisioned yet", grant.Name)
	}
	var secret corev1.Secret
	if err := b.Client.Get(ctx, types.NamespacedName{Namespace: grant.Status.Namespace, Name: grant.Status.TokenSecret}, &secret); err != nil {
		return nil, fmt.Errorf("reading token secret: %w", err)
	}
	token := secret.Data[corev1.ServiceAccountTokenKey]
	if len(token) == 0 {
		return nil, fmt.Errorf("token secret %s/%s not yet populated", grant.Status.Namespace, grant.Status.TokenSecret)
	}
	ca := b.ExternalCA
	if len(ca) == 0 {
		ca = secret.Data[corev1.ServiceAccountRootCAKey]
	}

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["provider"] = &clientcmdapi.Cluster{
		Server:                   b.ExternalAddress,
		CertificateAuthorityData: ca,
	}
	cfg.AuthInfos["kbind"] = &clientcmdapi.AuthInfo{Token: string(token)}
	cfg.Contexts["provider"] = &clientcmdapi.Context{
		Cluster:   "provider",
		AuthInfo:  "kbind",
		Namespace: grant.Status.Namespace,
	}
	cfg.CurrentContext = "provider"
	return clientcmd.Write(*cfg)
}

// Objects returns the bundle's objects: Secret + Connection + ClusterBinding,
// all named after the Grant, exactly the core's one-apply contract. Every
// path through the service layer terminates here.
func (b *BundleBuilder) Objects(grant *iamv1alpha1.Grant, kubeconfig []byte) []client.Object {
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: KonnectorNamespace, Name: grant.Name},
		StringData: map[string]string{"kubeconfig": string(kubeconfig)},
	}
	connection := &corev1alpha1.Connection{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1alpha1.GroupVersion.String(), Kind: "Connection"},
		ObjectMeta: metav1.ObjectMeta{Name: grant.Name},
		Spec: corev1alpha1.ConnectionSpec{
			KubeconfigSecretRef: corev1alpha1.SecretKeyRef{
				Namespace: KonnectorNamespace,
				Name:      grant.Name,
				Key:       "kubeconfig",
			},
		},
	}
	binding := &corev1alpha1.ClusterBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1alpha1.GroupVersion.String(), Kind: "ClusterBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: grant.Name},
		Spec: corev1alpha1.BindingSpec{
			ConnectionRef:    corev1alpha1.ConnectionRef{Name: grant.Name},
			APIs:             grant.Spec.APIs,
			ConflictPolicy:   grant.Spec.ConflictPolicy,
			RelatedResources: grant.Spec.RelatedResources,
		},
	}
	return []client.Object{secret, connection, binding}
}
