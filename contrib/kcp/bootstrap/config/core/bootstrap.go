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

package core

import (
	"context"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kube-bind/kube-bind/contrib/kcp/bootstrap/config/core/resources"
)

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
	return resources.Bootstrap(ctx, kcpClientSet, discoveryClient, dynamicClient, crdClient, batteriesIncluded)
}
