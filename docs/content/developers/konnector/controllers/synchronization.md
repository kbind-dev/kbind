# Synchronization Controllers

The managed-CRD controller watches kbind-managed `CustomResourceDefinitions` and their `Connections` in the **consumer cluster**. It starts a dynamic instance syncer for each selected group/version/resource, or GVR. Both are implemented in `engine/sync`.

They are responsible for:

* Starting, stopping, and rebuilding syncers as CRDs and provider connections change.
* Selecting instances covered by a Ready ClusterBinding or namespaced Binding.
* Applying consumer spec to owned provider objects and copying provider status back.
* Reporting ownership conflicts without overwriting foreign objects.
* Deleting owned provider copies before releasing consumer instance finalizers.

## Overview

The managed-CRD controller resolves the provider from the CRD's `core.kbind.io/connection` annotation. A missing annotation or an API error is returned for retry.

```mermaid
flowchart TD
    start(["Managed CRD or Connection event"])
    crd{"CRD exists<br/>and is not deleting?"}
    connection{"Connection Ready<br/>and localClusterUID set?"}
    engaged{"Provider client and cache<br/>engaged?"}
    current{"Existing syncer has current generation<br/>and the same provider instance?"}
    stop_old(["Stop the previous syncer"])
    version(["Choose storage or served version<br/>Build consumer informer and instance controller"])
    watches(["Watch consumer and provider instances<br/>Watch bindings covering this API"])
    run(["Start controller and informer<br/>Wait for cache sync and record syncer"])
    stop_syncer(["Stop existing syncer"])
    wait_provider(["Stop existing syncer<br/>Requeue after 2 seconds"])
    stop(["Stop"])

    start --> crd
    crd -->|no| stop_syncer
    crd -->|yes| connection
    connection -->|no| stop_syncer
    connection -->|yes| engaged
    engaged -->|no| wait_provider
    engaged -->|yes| current
    current -->|yes| stop
    current -->|no| stop_old
    stop_old --> version
    version --> watches
    watches --> run
    run --> stop
    stop_syncer --> stop
    wait_provider --> stop
```

The generation check detects schema changes. Comparing the provider instance also detects re-engagement after an outage, so a syncer cannot keep the old client/cache simply because the CRD generation is unchanged.

## Instance reconciliation

Consumer events, binding events, and provider cache events enqueue instances. Provider events are filtered by consumer-cluster UID and mapped back to the consumer object key. Reads of provider objects use the engaged cluster's API reader, while writes use its client.

```mermaid
flowchart TD
    start(["Consumer, provider, or binding event"])
    object{"Consumer object<br/>exists?"}
    resolve(["Resolve covering binding<br/>ClusterBinding before namespaced Binding"])
    deleting{"Consumer object<br/>deleting?"}
    cleanup(["Run instance deletion"])
    bound{"Covering binding<br/>Ready?"}
    map_key(["Map consumer key to provider key"])
    finalizer{"Syncer finalizer<br/>already present?"}
    add_finalizer(["Add finalizer and requeue"])
    target(["Read provider object"])
    ownership{"Provider target ownership?"}
    adopt{"conflictPolicy<br/>is Adopt?"}
    conflict(["Record conflict annotation<br/>and Warning Event"])
    namespace(["Ensure provider namespace<br/>for a new namespaced object"])
    apply(["Clear old conflict marker<br/>Apply spec and ownership markers"])
    status(["Read provider object again<br/>Copy status if present"])
    stop(["Stop"])

    start --> object
    object -->|no| stop
    object -->|yes| resolve
    resolve --> deleting
    deleting -->|yes| cleanup
    cleanup --> stop
    deleting -->|no| bound
    bound -->|no| stop
    bound -->|yes| map_key
    map_key --> finalizer
    finalizer -->|no| add_finalizer
    add_finalizer --> stop
    finalizer -->|yes| target
    target --> ownership
    ownership -->|absent| namespace
    namespace --> apply
    ownership -->|ours| apply
    ownership -->|no ownership markers| adopt
    ownership -->|owned by another| conflict
    adopt -->|yes| apply
    adopt -->|no| conflict
    conflict --> stop
    apply --> status
    status --> stop
```

Ownership requires both the consumer-cluster UID and consumer-object UID. `Adopt` accepts only markerless objects, never objects owned by another consumer/object. Spec apply uses server-side apply with forced field ownership. An absent provider status does not clear an existing consumer status.

Forbidden namespace creation or spec apply emits a Warning Event and retries after 30 seconds. Other API errors are returned for retry. Successful instance reconciliation schedules a ten-minute backstop, with watches and the consumer informer's resync providing additional triggers.

## Instance deletion

```mermaid
flowchart TD
    start(["Deleting consumer object"])
    finalizer{"Syncer finalizer<br/>present?"}
    orphan{"deletion-policy<br/>is Orphan?"}
    read_provider(["Read mapped provider object"])
    owned{"Provider copy exists<br/>and is ours?"}
    delete_copy(["Request provider deletion<br/>Requeue after 2 seconds"])
    release(["Remove consumer syncer finalizer"])
    stop(["Stop"])

    start --> finalizer
    finalizer -->|no| stop
    finalizer -->|yes| orphan
    orphan -->|yes| release
    orphan -->|no| read_provider
    read_provider --> owned
    owned -->|yes| delete_copy
    delete_copy --> stop
    owned -->|no| release
    release --> stop
```

This path runs even if the binding is no longer Ready. An owned provider object must disappear before its consumer finalizer is released. Provider read/delete errors are retried, not treated as absence. Foreign objects are left untouched.

The stock `Mapper` preserves scope, namespace, and name. It is a compile-time extension point for instance keys, not a CRD isolation flag. Related-resource sync remains in binding reconciliation and does not use that mapper.

See [Resource Synchronization](../../../usage/synchronization.md) for conflict policy, finalizers, related-resource selection, and supported behavior.

Implementation: [managed-CRD controller](https://github.com/kbind-dev/kbind/blob/v2-next/engine/sync/crd_controller.go), [binding resolution](https://github.com/kbind-dev/kbind/blob/v2-next/engine/sync/resolve.go), and [instance syncer](https://github.com/kbind-dev/kbind/blob/v2-next/engine/sync/syncer.go).
