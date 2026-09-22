---
title: API Concepts
description: >
  Core API types, their relationships, and cross-cluster service binding.
weight: 210
---

# kbind API Concepts

This guide provides an overview of kbind's core API types, their relationships, and how they work together to enable cross-cluster service binding.

## Overview

The v2 slim core is three consumer-side resources in **`core.kbind.io/v1alpha1`**. It connects Kubernetes APIs using declarative objects, it is not an API request proxy.

| Kind | Scope | Responsibility |
| --- | --- | --- |
| `Connection` | Cluster | Reference provider credentials, pin cluster identity, discover exported APIs, and choose schema policy. |
| `ClusterBinding` | Cluster | Activate synchronization for listed APIs across the consumer. |
| `Binding` | Namespace | Activate synchronization for listed namespaced APIs in this namespace. |

The konnector reconciles them in the consumer. A plain Kubernetes provider needs its own API/controller, credentials and RBAC, and exported CRD labels, not these core CRDs. The optional [backend](../developers/backend/index.md) and [catalog](catalog.md) add onboarding and discovery workflows without changing this core contract.

See the [core API reference](../reference/crd/index.md#corekbindiov1alpha1) for field definitions. These pages describe the current, unreleased implementation, policy limitations below matter even where the API types describe a broader intent.

## Connection

**Purpose**: Establish the provider link and choose schema policy. **Used by**: Service consumers. **Scope**: Cluster-scoped.

### Structure

```yaml
apiVersion: core.kbind.io/v1alpha1
kind: Connection
metadata:
  name: provider
spec:
  kubeconfigSecretRef:
    namespace: kbind
    name: provider-kubeconfig
    key: kubeconfig
  schema:
    source: CRD
    pullPolicy: Bound
    updatePolicy: Always
---
apiVersion: core.kbind.io/v1alpha1
kind: Binding
metadata:
  name: widgets
  namespace: team-a
spec:
  connectionRef:
    name: provider
  apis:
    - name: widgets.example.org
  conflictPolicy: Fail
```

Create `team-a` separately. The Connection reference has no namespace because Connections are cluster-scoped. Each `apis[].name` is `<plural>.<group>`, not a Kind, a version, or a URL.

The Connection can exist before its Secret, and the Binding before its Connection, controllers retry when dependencies appear. The CRD definitions themselves must already exist.

### Credentials and cluster identity

`spec.kubeconfigSecretRef` is the only credential reference in the core. It contains a required Secret namespace and name and an optional key defaulting to `kubeconfig`. The Secret may be in any namespace the konnector is authorized to read, it is not implicitly resolved in the Binding namespace.

The **reference is immutable**, but the Secret data can be updated. Keep credentials self-contained and usable in the konnector's environment. A kubeconfig that relies on your workstation's files, exec plugin, or interactive login is not automatically usable inside the stock container.

On first successful connection, the engine records:

- `status.remoteClusterUID`: provider identity.
- `status.localClusterUID`: consumer identity.

It identifies a cluster using the `core.kcp.io/v1alpha1` LogicalCluster named `cluster` when available, otherwise the `kube-system` Namespace UID. A forbidden identity read is reported rather than silently skipped. The API makes each recorded UID immutable once set.

Subsequent reconciliations compare the provider identity with the pinned UID. Replacing Secret data with a kubeconfig for a different cluster produces `ClusterIdentityChanged`, it must not silently move existing objects to a new provider. Use a new Connection and a deliberate unbind/rebind workflow for a new provider identity.

**Credential rotation limitation:** the Connection rereads Secret data, but an already-engaged instance-sync client is not rebuilt merely because a valid Secret changed while the Connection stayed Ready. After rotating credentials for the same provider, restart the konnector (or its deployment) and verify instance sync before revoking the old credential. The core does not renew tokens itself.

The engine adds `core.kbind.io/cleanup` to the Connection and its referenced Secret. The Secret finalizer keeps credentials available during teardown, the engine does not delete the Secret for you.

### Provider export boundary

With the CRD source, label an API on the provider:

```bash
kubectl --context=provider label crd widgets.example.org \
  core.kbind.io/exported=true --overwrite
```

Only the exact value `"true"` matches. Discovery normally refreshes every 30 seconds and appears in `Connection.status.exportedAPIs`.

This is an opt-in discovery filter, not an RBAC mechanism. A user with CRD read permission can still read unlabelled CRDs outside kbind. The konnector also needs independent instance permissions.

With OpenAPI, there is no exported-label check. The discovery/API boundary exposed by the provider credentials determines candidates, built-in Kubernetes groups are excluded. Not all discovery responses are filtered by object-level RBAC, so a discovered API is not proof that its instances can be read or written.

### Schema source and policies

Defaults are `source: Auto`, `pullPolicy: Bound`, `updatePolicy: Always`, and `autoBind: false`.

#### Source

| Source | Current behavior |
| --- | --- |
| `CRD` | List provider CRDs labelled `core.kbind.io/exported: "true"` and read selected CRD definitions. Recommended for an explicit opt-in on ordinary Kubernetes. |
| `OpenAPI` | Discover non-built-in APIs and synthesize consumer CRDs using `/openapi/v3`. Useful for CRD-less providers. No CRD label gate. |
| `Auto` | Try labelled CRD discovery first. A successful list with exports selects CRD, a successful list with no exports falls through to OpenAPI. |

**Auto is not a fallback for a failed CRD list.** The current code returns list errors, including `Forbidden`, instead of falling back. Choose `OpenAPI` explicitly when CRDs cannot be listed. Conversely, use `CRD` explicitly when an empty label selection should export nothing: Auto can otherwise discover unlabelled APIs through OpenAPI.

Inspect `status.activeSchemaSource` rather than assuming which source Auto selected.

#### Which schemas are installed?

| `pullPolicy` | Effect |
| --- | --- |
| `Bound` | Install only APIs named by a Binding or ClusterBinding. |
| `All` | Install all discovered exported APIs, even without bindings. |
| `None` | Do not create missing consumer CRDs, an external manager supplies them. |

Installing a schema does not activate instance sync. A Binding/ClusterBinding is still required unless `autoBind` supplies one.

With `None` and the **CRD** source, the engine still reads the provider CRD and stamps existing consumer CRDs with its managed and Connection markers. With the **OpenAPI** source, existing CRDs are only read: you must supply the markers yourself for the sync controller to discover them:

```yaml
metadata:
  labels:
    core.kbind.io/managed: "true"
  annotations:
    core.kbind.io/connection: provider
```

`None` does **not** protect a manually supplied CRD against ClusterBinding cleanup. See [GitOps](gitops.md) before sharing CRD ownership.

#### Schema updates and fidelity

`updatePolicy: Always` follows provider CRD spec changes for the CRD source. `Once` prevents subsequent spec updates. Neither is a compatibility check or a storage migration strategy.

Current limitations:

- A CRD-source pull keeps **one version**: the provider storage version, or the first served version if no storage version is found. It serves that version locally and forces conversion to `None`, removing the conversion webhook configuration.
- Validation, defaults, and subresources present in that selected CRD version can be retained, but provider admission webhooks and controllers are not installed. Local acceptance does not guarantee provider acceptance.
- OpenAPI synthesis can expose multiple discovered versions, with the discovery-preferred version selected for storage. It installs no conversion webhook, this does not reproduce provider-side multi-version conversion.
- OpenAPI `$ref` schemas are replaced with permissive object schemas preserving unknown fields, not fully resolved. CEL/defaulting fidelity and provider admission behavior are not guaranteed. Short names, printer columns, and scale subresources are not reconstructed, discovered status subresources are.
- With **OpenAPI + Bound**, the binding only synthesizes when the consumer CRD is absent. `Always` currently does not refresh an existing bound-only OpenAPI CRD. The `All` path does reconcile synthesized schemas repeatedly.
- `Once` does not stamp an arbitrary existing CRD into management. Use an intentional externally managed workflow rather than assuming any preinstalled schema is adopted.

Review consumer CRDs and run representative API validation/sync tests before exposing a provider API to applications. The provider remains authoritative for execution and admission.

## ClusterBinding and Binding

**Purpose**: Activate synchronization for the selected APIs. **Used by**: Service consumers. **Scope**: Cluster-wide (`ClusterBinding`) or one namespace (`Binding`).

### Namespace selection and overlapping bindings

A ClusterBinding covers all namespaces for a namespaced API, or the entire API for a cluster-scoped resource. A Binding covers only its own namespace and cannot activate cluster-scoped instance sync. There is no namespace label selector, namespace allowlist field, per-instance selector, or runtime rename/scope-conversion policy in the core.

The stock mapper preserves namespace and name. `team-a/my-widget` on the consumer becomes `team-a/my-widget` on the provider. A provider kubeconfig's default namespace controls the heartbeat location, **not** this mapping.

The CRD remains cluster-scoped even when a Binding is namespaced. Users in other namespaces can see/use that API if their local RBAC permits it, but their instances do not sync without a covering binding. Informers still read cluster-wide.

Avoid overlapping bindings for an API. The resolver checks ClusterBindings before namespaced Bindings, including when the ClusterBinding is not Ready. It returns the first match at a given scope, there is no supported priority or merge policy among peers. A managed consumer CRD pins one Connection for its syncer, so binding the same API to different providers is not supported routing.

### Automatic binding

`spec.autoBind: true` makes the engine maintain a ClusterBinding **named after the Connection**, listing every exported API and owned by that Connection. Newly discovered exports can therefore start syncing without another user action. Reserve that name, do not also manage it as an independent GitOps object.

When there are no exports, the reconciler deletes that ClusterBinding. Changing `autoBind` to `false` stops maintaining it but currently does **not** remove an already-created binding. Delete that binding deliberately if the goal is to stop syncing.

## Export, Collection, and Grant

The optional service layer uses `Export` to describe an offering, `Collection` to group offerings, and `Grant` to record issued credentials. They are cluster-scoped resources on the provider, in the `catalog.kbind.io` and `iam.kbind.io` groups. See [Catalog and Grants](catalog.md) for their structures.

## Complete Binding Flow

1. **Provider definition**: expose service APIs and credential RBAC, optionally with a catalog Export.
2. **Consumer request**: bind an Export through the optional CLI/backend, or prepare core manifests directly.
3. **Provider processing**: the optional issuer provisions a Grant's credentials.
4. **Consumer binding**: apply the Secret, Connection, and bindings with the konnector installed.
5. **Bidirectional sync**: consumer spec flows up and provider status flows down.

### Observing progress

Connection status reports identities, active schema source, exports, and conditions such as `SecretValid`, `Connected`, `PermissionDenied`, and `Ready`. Although `SchemaInSync` is defined in the API, the current Connection reconciler does not populate it.

Binding status reports `boundAPIs`, schema hashes, conflict counts, and conditions including `Ready`, `Synced`, `Conflicts`, and `PermissionDenied`. `Synced=True` is a setup-level result, not a per-object delivery acknowledgement. Conflicting objects can coexist with a ready binding, some instance RBAC errors appear only as Events/logs.

## Related Documentation

Use [Synchronization](synchronization.md) for the data path and lifecycle, and [Troubleshooting](troubleshooting.md) to inspect actual object delivery.
