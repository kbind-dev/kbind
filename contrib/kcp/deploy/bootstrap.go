/*
Copyright 2025 The Kube Bind Authors.

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

package kubebind

import (
	"context"
	"embed"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclient "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kube-bind/kube-bind/contrib/kcp/deploy/resources"
)

//go:embed examples/*.yaml
var Examples embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(
	ctx context.Context,
	kcpClientSet kcpclientset.ClusterInterface,
	apiExtensionClusterClient kcpapiextensionsclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	batteriesIncluded sets.Set[string],
	baseWorkspace logicalcluster.Path,
) error {
	discoveryClient := apiExtensionClusterClient.Cluster(baseWorkspace).Discovery()
	dynamicClient := dynamicClusterClient.Cluster(baseWorkspace)

	crdClient := apiExtensionClusterClient.ApiextensionsV1().Cluster(baseWorkspace).CustomResourceDefinitions()
	kcpClient := kcpClientSet.Cluster(baseWorkspace)

	err := resources.Bootstrap(ctx, kcpClientSet, discoveryClient, dynamicClient, crdClient, batteriesIncluded)
	if err != nil {
		return err
	}

	// create recursive apibinding so we can start controllers.
	// this is a temporary solution until we have a better way to bootstrap controllers.
	return bindAPIExport(ctx, kcpClient, "kube-bind.io", baseWorkspace)
}

func bindAPIExport(ctx context.Context, kcpClient kcpclient.Interface, exportName string, clusterPath logicalcluster.Path) error {
	logger := klog.FromContext(ctx)

	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: clusterPath.String(),
					Name: exportName,
				},
			},
		},
	}

	binding.Spec.PermissionClaims = []apisv1alpha2.AcceptablePermissionClaim{
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "rbac.authorization.k8s.io",
						Resource: "clusterrolebindings",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "rbac.authorization.k8s.io",
						Resource: "clusterroles",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "rbac.authorization.k8s.io",
						Resource: "roles",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "rbac.authorization.k8s.io",
						Resource: "rolebindings",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "",
						Resource: "serviceaccounts",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "",
						Resource: "configmaps",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "",
						Resource: "secrets",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    apisv1alpha2.SchemeGroupVersion.Group,
						Resource: "apiexports",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "apiextensions.k8s.io",
						Resource: "customresourcedefinitions",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "",
						Resource: "namespaces",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "apis.kcp.io",
						Resource: "apiresourceschemas",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "apis.kcp.io",
						Resource: "apibindings",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "authorization.k8s.io",
						Resource: "subjectaccessreviews",
					},
					Verbs: []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
	}

	_, err := kcpClient.ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	if err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		existing, err := kcpClient.ApisV1alpha2().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "error getting APIBinding", "name", exportName)
			// Always keep trying. Don't ever return an error out of this function.
			return false, nil
		}

		logger.V(2).Info("Updating API binding")
		existing.Spec = binding.Spec

		_, err = kcpClient.ApisV1alpha2().APIBindings().Update(ctx, existing, metav1.UpdateOptions{})
		if err == nil {
			return true, nil
		}
		if apierrors.IsConflict(err) {
			logger.V(2).Info("API binding update conflict, retrying")
			return false, nil
		}

		logger.Error(err, "error updating APIBinding")
		// Always keep trying. Don't ever return an error out of this function.
		return false, nil
	}); err != nil {
		return err
	}

	return nil
}
