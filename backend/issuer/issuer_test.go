/*
Copyright 2026 The kbind Authors.

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
	"testing"

	"github.com/stretchr/testify/require"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
)

func TestIdentityNamingIsStable(t *testing.T) {
	subject := "https://issuer.example.com#alice"

	require.Equal(t, BoundaryNamespace(subject), BoundaryNamespace(subject),
		"same human, same boundary")
	require.Equal(t, GrantName("mangodb", subject), GrantName("mangodb", subject))

	require.NotEqual(t, BoundaryNamespace(subject), BoundaryNamespace("https://issuer.example.com#bob"))
	require.NotEqual(t, GrantName("mangodb", subject), GrantName("postgres", subject),
		"one grant per (export, identity)")
}

func TestSplitAPIName(t *testing.T) {
	resource, group := SplitAPIName("mangodbs.mangodb.io")
	require.Equal(t, "mangodbs", resource)
	require.Equal(t, "mangodb.io", group)
}

func TestGrantRulesEnumerateExactly(t *testing.T) {
	issuer := &KubeIssuer{}
	rules := issuer.grantRules(&iamv1alpha1.Grant{
		Spec: iamv1alpha1.GrantSpec{
			APIs: []corev1alpha1.APIRef{{Name: "widgets.example.org"}},
			RelatedResources: []corev1alpha1.RelatedResource{
				{Resource: "secrets", Direction: corev1alpha1.FromProvider},
				{Resource: "configmaps", Direction: corev1alpha1.FromConsumer},
			},
		},
	})

	require.Len(t, rules, 3)
	require.Equal(t, []string{"example.org"}, rules[0].APIGroups)
	require.Equal(t, []string{"widgets", "widgets/status"}, rules[0].Resources)
	require.Equal(t, []string{"get", "list", "watch"}, rules[1].Verbs,
		"FromProvider related resources are read-only for the consumer credentials")
	require.Contains(t, rules[2].Verbs, "create",
		"FromConsumer related resources are written by the konnector")
}
