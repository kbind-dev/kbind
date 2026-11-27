---
title: Resource Synchronization
description: >
  Resource synchronization, ownership, and lifecycle between consumer and provider clusters.
weight: 220
---

# Resource Synchronization

This section outlines how kbind synchronizes objects between two kinds of clusters:

* The **provider cluster** is run by a service provider and hosts a service, operator, or other Kubernetes API. The optional backend offers these APIs through a catalog.
* The **consumer clusters** are where end users consume services/APIs offered by providers.

There is a 1:n relationship: one provider can serve many consumer clusters.

## Overview

The konnector turns managed consumer CRDs into per-resource sync controllers. A Ready Connection supplies the provider client, and a Ready Binding or ClusterBinding selects which consumer instances participate. See [API concepts](api-concepts.md) for scope, schema delivery, and overlapping-binding restrictions.

### Sync Direction

The consumer clusters represent the source of truth for desired state, and the provider holds copies of those objects. During synchronization, spec is copied from consumer to provider and status is copied in the opposite direction.

| Data | Direction | Behavior |
| --- | --- | --- |
| Bound instance `spec` | Consumer → provider | Server-side apply using field manager `kbind-konnector`, with force ownership after the object-ownership check. |
| Bound instance `status` | Provider → consumer | Copies the provider status through the consumer status subresource. |
| Related Secret/ConfigMap payload | Configured per entry | Server-side apply using `kbind-konnector-related`. |

Instance namespace/name are unchanged. The engine does not copy arbitrary consumer labels, annotations, owner references, or finalizers to the provider, it writes its own management and ownership metadata. Top-level fields other than `spec` are not a generic object replication interface.

Status is copied when the provider object contains it. Absence of provider status does not actively clear an existing consumer status. APIs intended for status round-tripping need an appropriate status schema/subresource on the consumer.

Consumer changes and provider watch events drive synchronization. Provider events also allow desired consumer specs to repair provider drift. There are periodic resyncs as a backstop, this is asynchronous reconciliation, not a cross-cluster transaction or a synchronous admission guarantee.

### Connectivity

The object synchronization logic lives in the konnector, an agent running on each consumer cluster. It reads a local Secret containing a kubeconfig for the provider. Connections and bindings in the consumer describe what to synchronize.

This design allows consumer clusters to be mostly firewalled off, but requires provider API servers to be reachable from consumers. No inbound provider connection to the consumer is needed for core synchronization.

### RBAC

Provider credentials govern what the konnector may access. Object ownership checks do not protect against direct use of overprivileged credentials. A namespaced Binding restricts sync selection but does not make the current dynamic informers namespace-scoped. See [RBAC and credentials](../developers/konnector/index.md#rbac-and-credentials).

## Cluster Isolation

Namespaced resources retain the same namespace and name on each side. Cluster-scoped resources remain cluster-scoped. The built-in v2 mapper does not provide `Prefixed` or `Namespaced` isolation strategies, choose compatible provider namespaces, credentials, or dedicated clusters/workspaces instead.

## Ownership and conflicts

Provider instance copies carry:

```yaml
metadata:
  labels:
    core.kbind.io/managed: "true"
  annotations:
    core.kbind.io/consumer-cluster-uid: "<consumer-cluster-uid>"
    core.kbind.io/consumer-object-uid: "<consumer-object-uid>"
```

The ownership check uses **both UIDs**, not just the managed label or the object's name. Instance ownership is tied to the source object, not to a Binding UID.

`spec.conflictPolicy` on a binding controls a preexisting provider target:

- **`Fail`** (default): leave the target untouched and report a conflict.
- **`Adopt`**: take ownership only when both consumer ownership annotations are absent. This can overwrite fields through forced server-side apply, so use it only after reviewing the existing object.
- Neither policy takes an object carrying another consumer/object's ownership markers. Partial markers also count as foreign ownership.

A conflict produces `core.kbind.io/conflict` on the consumer instance and a Warning Event (`ForeignObjectExists` or `OwnedByAnother`). A best-effort `Synced=False` instance condition may be pruned by the API's status schema, the annotation is the durable diagnostic.

Bindings count conflict annotations on a periodic reconciliation, normally every 30 seconds. `Conflicts=True` does not necessarily make `Ready` or `Synced` false, and unrelated objects can continue syncing.

Deleting a conflicting consumer object does not delete the foreign provider object. Recreating an orphaned consumer instance gives it a new UID, so its retained provider copy will not automatically become owned by the new instance.

## Related Secrets and ConfigMaps

Add `spec.relatedResources` to either binding kind:

```yaml
relatedResources:
  - group: ""
    resource: secrets
    direction: FromProvider
    selector:
      labelSelector:
        matchLabels:
          widgets.example.org/related: "true"
  - group: ""
    resource: configmaps
    direction: FromConsumer
    selector:
      names:
        - widget-settings
```

Use the core group (`""`), only `secrets` and `configmaps` are supported. `FromProvider` copies provider → consumer, `FromConsumer` copies consumer → provider.

Selection and copying are deliberately simple:

- A Binding selects within its namespace, a ClusterBinding lists across namespaces.
- Names match exactly. If names and a label selector are both provided, **both** must match.
- An omitted/empty selector matches everything in scope. Avoid this for Secrets.
- Selection is independent of individual API instances. There is no JSONPath/reference following or owner-reference traversal.
- Namespace/name are preserved. The payload fields copied are `data`, `stringData`, `binaryData`, `type`, and `immutable` when present, not arbitrary source metadata.
- Copies carry the binding UID in `core.kbind.io/related-binding`.
- A target with a different or absent related-binding marker is left untouched, even under `conflictPolicy: Adopt`. These collisions are silently skipped and are not included in instance conflict counts.

Related-resource sync runs in binding reconciliation, normally every 30 seconds, rather than using the instance watch path. Source changes may take that long to appear.

Copies belonging to the binding are deleted when they no longer match the current selector or when the binding is deleted. Source objects are not deleted. Use **one entry per resource kind and direction**: current garbage collection is keyed by binding UID and kind, not individual selector, so multiple entries for the same target kind/direction can delete each other's copies. Likewise, removing an entire entry does not run cleanup for that removed entry, clean up its copies before removing it.

Related copies are not exempted by an instance's deletion policy. Namespace creation and source/target RBAC are still required. Review who can label provider Secrets before selecting them into consumers.

## Heartbeat and disengagement

On a successful Connection reconciliation, normally every 30 seconds, the engine creates or renews a plain `coordination.k8s.io/v1` Lease on the provider:

- Namespace: the provider kubeconfig's current-context namespace, or `kbind` if unset.
- Name: Connection name plus `-` and the first ten characters of the local cluster UID.
- `holderIdentity`: the full consumer cluster UID.
- `leaseDurationSeconds`: `60`.
- Managed label and consumer-UID/Connection annotations.

This heartbeat is distinct from the konnector's consumer-side leader-election Lease. Heartbeat failure is best-effort: it does not block instance sync or make the Connection unready. The core neither reaps expired Leases nor garbage-collects abandoned consumers based on them, that requires an optional [backend](../developers/backend/index.md) reaper or your own operations.

When a Connection becomes not Ready, the provider cache is disengaged and its per-API instance syncers stop. When it becomes Ready again, syncers are rebuilt against the newly engaged provider. Objects and CRDs are not deleted merely because connectivity is lost. This is different from unbinding.

There is no `suspend` field. Do not use credential corruption as a pause mechanism. For planned downtime, stop the konnector and understand that existing finalizers remain, for permanent removal, use the cleanup sequence below.

## Unbinding and deletion

### Delete one consumer instance

The engine places `core.kbind.io/syncer` on the consumer instance before its first provider write. On normal deletion it:

1. Reads the provider target and checks both ownership UIDs.
2. Deletes only a copy it owns.
3. Waits for the provider object to disappear before releasing the consumer finalizer.

An unreachable provider or a provider-side finalizer can therefore hold normal instance deletion open.

To keep an instance's provider copy, set the exact annotation value **before** deleting the consumer instance or unbinding:

```bash
kubectl --context=consumer -n team-a annotate widget my-widget \
  core.kbind.io/deletion-policy=Orphan --overwrite
```

`Orphan` releases the instance finalizer without deleting the provider copy. It does not remove the provider ownership markers and does not keep a consumer instance alive when its CRD is deleted.

### Delete a binding

Bindings use `core.kbind.io/cleanup`. Deleting one stops it being selected for new instance sync and runs cleanup:

| Deleted object | Expected cleanup when no other binding lists the API |
| --- | --- |
| Namespaced Binding | Delete owned provider instance copies in that namespace, release consumer instance finalizers, retain the shared CRD and consumer instances. |
| ClusterBinding | Drain instances across the API, then delete the consumer CRD, which deletes its consumer instances. |
| Either | Delete owned related-resource copies for the currently declared related-resource entries. |

**This is destructive for a last ClusterBinding**, including a CRD installed manually with `pullPolicy: None`. The cleanup path does not limit CRD deletion to schemas originally created by that binding.

Current cleanup limits:

- If any other non-deleting binding anywhere lists the same API, cleanup skips that API as a whole. It does not calculate remaining namespace/Connection coverage. Overlapping bindings can therefore leave provider copies or consumer finalizers outside the remaining coverage.
- Instance cleanup is best-effort if credentials cannot be built or provider reads fail, provider copies may remain. Actual provider delete errors can still block cleanup.
- Unlike normal instance deletion, binding cleanup does not wait for every provider object's finalizers to finish after requesting deletion.
- Namespaces and heartbeat Leases are not removed. An unbound schema pulled only by `pullPolicy: All` is not automatically removed on Connection deletion.
- Editing `apis`, `connectionRef`, or related-resource entries is not a cleanup transaction for the previous spec. Remove old instances/copies deliberately before changing scope or provider.

Back up resources, avoid overlapping ownership, and verify both sides after teardown.

### Delete the Connection last

Keep the controller running and credentials valid throughout:

```bash
kubectl --context=consumer -n team-a delete bindings.core.kbind.io widgets --timeout=120s
kubectl --context=consumer delete connection provider --timeout=120s
kubectl --context=consumer -n kbind delete secret provider-kubeconfig
```

Use `delete clusterbinding` instead when that is what you created.

A deleting Connection waits for **all referencing bindings** to disappear. It does not delete your explicit bindings, `Ready=False` with reason `DrainingBindings` means you must finish their teardown. A Connection with `autoBind: true` explicitly deletes its generated ClusterBinding as part of deletion.

After references drain, the Connection releases its own cleanup finalizer and the credential Secret's finalizer, unless another active Connection references that Secret. Do not remove the konnector or the core CRDs before this completes.

Use `kubectl` for unbinding, the v2 CLI has no unbind command. Cleanup is implemented by these controllers. For stuck teardown, see [Troubleshooting](troubleshooting.md#deletion-is-stuck).
