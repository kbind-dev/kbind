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

// Package mapper defines the Mapper extension point: how the konnector
// translates object identity (namespace/name) between the consumer and the
// provider when syncing instances.
//
// Core ships exactly one implementation — Identity — and the interface is a
// compile-time seam so out-of-tree konnector builds can restore tenancy
// key-mapping (e.g. v1's "Prefixed" isolation) without forking the engine. It
// is deliberately kept out of the CRD API: the core API never promises
// renaming.
//
// The interface maps KEYS only; it cannot change an object's scope
// (cluster-scoped stays cluster-scoped). That is the hard line v2 draws — v1's
// "Namespaced" scope-conversion isolation is intentionally not expressible here.
// A later, separate Transformer interface (mutating the object payload — label
// injection, field stripping) is possible but deliberately deferred.
package mapper

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ObjectKey is a namespace/name identity. It aliases controller-runtime's
// client.ObjectKey for drop-in interop with the syncer.
type ObjectKey = client.ObjectKey

// Mapper translates object identity between consumer and provider. Core
// registers exactly one implementation: Identity.
type Mapper interface {
	// ToProvider maps a consumer object key to its provider key.
	ToProvider(gvr schema.GroupVersionResource, key ObjectKey) (ObjectKey, error)
	// ToConsumer is the inverse of ToProvider; it must round-trip, i.e.
	// ToConsumer(gvr, ToProvider(gvr, k)) == k.
	ToConsumer(gvr schema.GroupVersionResource, key ObjectKey) (ObjectKey, error)
}

// Identity is the core Mapper: the consumer ns/name equals the provider ns/name
// with no transformation. It is the only mapping core ships.
type Identity struct{}

// ToProvider returns key unchanged.
func (Identity) ToProvider(_ schema.GroupVersionResource, key ObjectKey) (ObjectKey, error) {
	return key, nil
}

// ToConsumer returns key unchanged.
func (Identity) ToConsumer(_ schema.GroupVersionResource, key ObjectKey) (ObjectKey, error) {
	return key, nil
}

var _ Mapper = Identity{}
