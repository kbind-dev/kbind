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

// Package api holds the gateway's wire types, shared by the server, the CLI
// and the UI. The protocol is deliberately thin: its terminal output is the
// core's one-apply bundle, nothing else — no request objects to poll, no
// phases. curl bind + pickup piped to kubectl apply is a complete client.
package api

import (
	"encoding/json"
	"time"
)

// Provider is GET /api/provider: provider metadata + supported auth methods.
type Provider struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	AuthMethods []string `json:"authMethods"`
	// ApplyEnabled reports whether the flag-gated POST /api/apply
	// (browser-apply) path is available.
	ApplyEnabled bool `json:"applyEnabled"`
}

// Catalog is GET /api/catalog: the offerings visible to the caller.
type Catalog struct {
	Exports     []Export     `json:"exports"`
	Collections []Collection `json:"collections,omitempty"`
}

// Export is one curated offering.
type Export struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	IconURL     string   `json:"iconURL,omitempty"`
	Docs        string   `json:"docs,omitempty"`
	APIs        []string `json:"apis"`
	// RelatedResources are the auxiliary objects (secrets/configmaps) a
	// binding to this offering syncs alongside the APIs — a core concept
	// (core.kbind.io relatedResources), surfaced here so consumers see what
	// flows before they bind.
	RelatedResources []RelatedResource `json:"relatedResources,omitempty"`
}

// RelatedResource is one auxiliary-object rule of an offering.
type RelatedResource struct {
	// Resource is "secrets" or "configmaps".
	Resource string `json:"resource"`
	// Direction is FromProvider or FromConsumer.
	Direction string `json:"direction"`
	// Selector is a human-readable summary of what is selected (labels
	// and/or names); empty means everything in scope.
	Selector string `json:"selector,omitempty"`
}

// Collection is a grouping of exports for browsing.
type Collection struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Exports     []string `json:"exports"`
}

// BindRequest is POST /api/bind's body.
type BindRequest struct {
	// Export is the catalog Export to bind.
	Export string `json:"export"`
}

// BindResponse is POST /api/bind's result: a one-time pickup URL for the
// bundle. Single-use and short-TTL applies to the pickup URL, not the
// credential inside — a picked-up bundle stays valid until its Grant is
// revoked (GitOps-safe).
type BindResponse struct {
	// Grant is the issuance record's name on the provider.
	Grant string `json:"grant"`
	// PickupURL is the single-use bundle pickup URL (relative to the
	// gateway base unless absolute).
	PickupURL string `json:"pickupURL"`
	// ExpiresAt is when the pickup URL dies.
	ExpiresAt time.Time `json:"expiresAt"`
}

// Bundle is GET /api/bundle/<token> with Accept: application/json — the same
// objects the YAML multi-doc carries, in a thin envelope.
type Bundle struct {
	Bundle []json.RawMessage `json:"bundle"`
}

// ApplyRequest is POST /api/apply's body (flag-gated browser-apply): the
// gateway applies the bundle into a consumer cluster using a caller-supplied
// kubeconfig. Consumer credentials transit the gateway — deployments enable
// this consciously.
type ApplyRequest struct {
	// Export is the catalog Export to bind. Optional when InstallKonnector
	// is set: an export-less apply is a pure "connect a cluster" (konnector
	// install only).
	Export string `json:"export,omitempty"`
	// Kubeconfig is the consumer cluster kubeconfig, base64-encoded.
	Kubeconfig string `json:"kubeconfig"`
	// InstallKonnector also installs/upgrades the konnector.
	InstallKonnector bool `json:"installKonnector,omitempty"`
}

// ApplyResponse reports what the gateway applied.
type ApplyResponse struct {
	Applied []string `json:"applied"`
}

// Clusters is GET /api/clusters: the consumer clusters known from konnector
// heartbeat Leases, and what each has bound. Grants whose bundle was never
// applied (no heartbeat yet) are listed separately as pending.
type Clusters struct {
	Clusters []Cluster      `json:"clusters"`
	Pending  []PendingGrant `json:"pending,omitempty"`
}

// Cluster is one consumer cluster, identified by the cluster UID the
// konnector pins on its heartbeat Lease.
type Cluster struct {
	// UID is the consumer cluster's stable identity.
	UID string `json:"uid"`
	// LastHeartbeat is the newest Lease renewal across the cluster's bindings.
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	// Live is true when at least one binding's heartbeat is current.
	Live bool `json:"live"`
	// Bindings are the grants this cluster actively consumes.
	Bindings []ClusterGrant `json:"bindings"`
}

// ClusterGrant is one grant as consumed by one cluster.
type ClusterGrant struct {
	Grant             string    `json:"grant"`
	Export            string    `json:"export"`
	Identity          string    `json:"identity,omitempty"`
	Subject           string    `json:"subject,omitempty"`
	BoundaryNamespace string    `json:"boundaryNamespace,omitempty"`
	LastHeartbeat     time.Time `json:"lastHeartbeat"`
	// Live is true while the heartbeat Lease is renewed within its grace
	// window (2x the lease duration).
	Live bool `json:"live"`
	// APIs are the bound APIs with the number of objects currently synced
	// onto the provider by this cluster (best-effort count).
	APIs []SyncedAPI `json:"apis"`
}

// SyncedAPI is one bound API and how many of its provider objects were
// written by the cluster.
type SyncedAPI struct {
	Name        string `json:"name"`
	SyncedCount int    `json:"syncedCount"`
}

// ExportInstances is GET /api/catalog/{export}/instances: the provider-side
// objects consumers have synced under one catalog item.
type ExportInstances struct {
	Export    string     `json:"export"`
	Instances []Instance `json:"instances"`
}

// Instance is one synced object on the provider, attributed to the consumer
// cluster (and, where a heartbeat links it, the identity) that wrote it.
type Instance struct {
	// API is the CRD name ("<plural>.<group>") the instance belongs to —
	// or "secrets"/"configmaps" for related resources.
	API string `json:"api"`
	// Related marks a related-resource row (a secret/configmap that flows
	// alongside the APIs) rather than a bound-API instance.
	Related bool `json:"related,omitempty"`
	// Direction is set on related rows: FromProvider rows are the provider
	// originals delivered to every bound cluster (the consumer-side copies
	// are invisible to the provider by design); FromConsumer rows are copies
	// consumers synced here, attributed like any synced instance.
	Direction string `json:"direction,omitempty"`
	// Namespace is empty for cluster-scoped instances.
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// ClusterUID is the consumer cluster that owns the object (from the
	// sync engine's ownership marker).
	ClusterUID string `json:"clusterUID,omitempty"`
	// Identity is the human behind the grant whose connection heartbeats
	// from that cluster, when the link can be made.
	Identity  string    `json:"identity,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// PendingGrant is a grant whose credentials were issued but whose bundle has
// never heartbeated — bound, not (yet) connected.
type PendingGrant struct {
	Grant     string    `json:"grant"`
	Export    string    `json:"export"`
	Identity  string    `json:"identity,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Error is any non-2xx response body.
type Error struct {
	Error string `json:"error"`
}
