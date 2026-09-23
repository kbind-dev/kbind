# Connection Controller

The Connection controller watches `Connections` and their referenced `Secrets` in the **consumer cluster**. It is implemented in `engine/connection`.

It is responsible for:

* Validating the provider kubeconfig and pinning provider/consumer identities.
* Discovering exported APIs and recording the active schema source.
* Installing schemas eagerly when `pullPolicy: All`.
* Maintaining an automatic ClusterBinding when `autoBind` is enabled.
* Renewing the provider heartbeat Lease and protecting credentials during cleanup.

## Overview

The chart below shows a non-deleting Connection. Successful reconciliation requeues after 30 seconds by default, so newly exported APIs are discovered without requiring a Secret or Connection edit.

```mermaid
flowchart TD
    start(["Connection or Secret event<br/>or periodic reconciliation"])
    get_connection(["Get Connection"])
    exists{"Connection<br/>exists?"}
    finalizers(["Ensure cleanup finalizers<br/>on Connection and available Secret"])
    added{"Connection finalizer<br/>just added?"}
    credentials{"Provider kubeconfig<br/>usable?"}
    invalid(["Set SecretValid=False<br/>and Ready=False"])
    identity(["Set SecretValid=True<br/>Read and verify cluster identities"])
    connected{"Provider identity readable<br/>and matches pinned UID?"}
    unavailable(["Record identity failure<br/>Set Ready=False"])
    pin(["Record cluster UIDs<br/>Set Connected=True"])
    discover(["Discover exported APIs<br/>Record activeSchemaSource and exportedAPIs"])
    pull_all{"pullPolicy<br/>is All?"}
    install(["Pull or synthesize all exported CRDs<br/>Honor updatePolicy"])
    auto_bind{"autoBind<br/>enabled?"}
    binding(["Update managed ClusterBinding<br/>Delete it if no APIs are exported"])
    heartbeat(["Attempt to renew provider Lease"])
    ready(["Set Ready=True"])
    status(["Persist changed status<br/>Requeue after 30 seconds"])
    stop(["Stop"])

    start --> get_connection
    get_connection --> exists
    exists -->|no| stop
    exists -->|yes| finalizers
    finalizers --> added
    added -->|yes| stop
    added -->|no| credentials
    credentials -->|no| invalid
    invalid --> status
    credentials -->|yes| identity
    identity --> connected
    connected -->|no| unavailable
    unavailable --> status
    connected -->|yes| pin
    pin --> discover
    discover --> pull_all
    pull_all -->|yes| install
    pull_all -->|no| auto_bind
    install --> auto_bind
    auto_bind -->|yes| binding
    auto_bind -->|no| heartbeat
    binding --> heartbeat
    heartbeat --> ready
    ready --> status
    status --> stop
```

Provider identity/discovery RBAC denials set `PermissionDenied=True` and `Ready=False`. Other API errors are returned for retry rather than continuing through the success path. Heartbeat errors are logged but do not prevent Ready.

`CRD` discovery uses the export label. `OpenAPI` uses discovery and OpenAPI v3. `Auto` tries CRDs first and switches to OpenAPI only after a successful list with no exports, not after a list error. `Bound` defers installation to binding reconciliation, and `None` leaves creation to an external manager.

## Deletion

```mermaid
flowchart TD
    start(["Deleting Connection"])
    finalizer{"Cleanup finalizer<br/>present?"}
    auto_bind{"autoBind<br/>enabled?"}
    remove_binding(["Request deletion of<br/>the managed ClusterBinding"])
    references{"Any bindings still<br/>reference this Connection?"}
    wait_bindings(["Set Ready=False with DrainingBindings<br/>Requeue after 2 seconds"])
    shared_secret{"Another non-deleting Connection<br/>uses the same Secret?"}
    release_secret(["Remove Secret cleanup finalizer<br/>if the Secret still exists"])
    release_connection(["Remove Connection cleanup finalizer"])
    stop(["Stop"])

    start --> finalizer
    finalizer -->|no| stop
    finalizer -->|yes| auto_bind
    auto_bind -->|yes| remove_binding
    auto_bind -->|no| references
    remove_binding --> references
    references -->|yes| wait_bindings
    wait_bindings --> stop
    references -->|no| shared_secret
    shared_secret -->|yes| release_connection
    shared_secret -->|no| release_secret
    release_secret --> release_connection
    release_connection --> stop
```

Explicit bindings are not deleted by this controller. They must be removed separately while credentials and the konnector are still available. Releasing the Secret finalizer does not itself delete the Secret.

See [API concepts](../../../usage/api-concepts.md) for schema policies and credential-rotation limits, and [lifecycle](../../../usage/synchronization.md) for heartbeat and deletion behavior.

Implementation: [Connection reconciler](https://github.com/kbind-dev/kbind/blob/v2-next/engine/connection/reconciler.go).
