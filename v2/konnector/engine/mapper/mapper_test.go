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

package mapper_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kbind/kbind/v2/konnector/engine/mapper"
)

var widgetGVR = schema.GroupVersionResource{Group: "example.org", Version: "v1", Resource: "widgets"}

func TestIdentity_RoundTrips(t *testing.T) {
	m := mapper.Identity{}
	in := mapper.ObjectKey{Namespace: "team-a", Name: "w1"}

	prov, err := m.ToProvider(widgetGVR, in)
	require.NoError(t, err)
	require.Equal(t, in, prov, "Identity must not change the key on the way to the provider")

	back, err := m.ToConsumer(widgetGVR, prov)
	require.NoError(t, err)
	require.Equal(t, in, back, "Identity must round-trip")
}

// prefixMapper is an out-of-tree-style Mapper that prefixes the provider
// namespace, exercising the interface the way a custom build would (v1's
// "Prefixed" isolation). It lives in the test to prove the seam is usable and
// that the round-trip contract is satisfiable by a non-identity mapping.
type prefixMapper struct{ prefix string }

func (p prefixMapper) ToProvider(_ schema.GroupVersionResource, key mapper.ObjectKey) (mapper.ObjectKey, error) {
	return mapper.ObjectKey{Namespace: p.prefix + key.Namespace, Name: key.Name}, nil
}

func (p prefixMapper) ToConsumer(_ schema.GroupVersionResource, key mapper.ObjectKey) (mapper.ObjectKey, error) {
	return mapper.ObjectKey{Namespace: strings.TrimPrefix(key.Namespace, p.prefix), Name: key.Name}, nil
}

func TestMapper_NonIdentityRoundTrips(t *testing.T) {
	var m mapper.Mapper = prefixMapper{prefix: "consumer-7-"}
	in := mapper.ObjectKey{Namespace: "team-a", Name: "w1"}

	prov, err := m.ToProvider(widgetGVR, in)
	require.NoError(t, err)
	require.Equal(t, "consumer-7-team-a", prov.Namespace)
	require.Equal(t, "w1", prov.Name)

	back, err := m.ToConsumer(widgetGVR, prov)
	require.NoError(t, err)
	require.Equal(t, in, back, "a Mapper must round-trip: ToConsumer(ToProvider(k)) == k")
}
