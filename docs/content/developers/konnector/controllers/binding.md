# Binding Controllers

The ClusterBinding and Binding controllers watch their respective binding objects and referenced `Connections` in the **consumer cluster**. Both use the reconciliation logic in `engine/binding`.

They are responsible for:

* Resolving a Ready Connection and validating the requested exported APIs.
* Installing or reading consumer schemas according to the Connection's policies.
* Synchronizing selected related Secrets and ConfigMaps.
* Reporting bound APIs, schema hashes, conflict counts, and readiness.
* Cleaning up instances and related-resource copies when a binding is deleted.

A ClusterBinding operates cluster-wide. A Binding limits instance selection and related-resource processing to its namespace, but does not change CRD scope or make the dynamic instance informers namespace-scoped.

## Overview

The chart shows normal reconciliation after the cleanup finalizer has been added. Binding and Connection events trigger reconciliation, with a 30-second periodic refresh for related resources and conflict counts.

```mermaid
flowchart TD
    start(["Binding or Connection event<br/>or periodic reconciliation"])
    get_binding(["Get binding"])
    exists{"Binding<br/>exists?"}
    connection{"Referenced Connection<br/>exists and is Ready?"}
    pending(["Set Ready=False<br/>with Pending"])
    provider(["Build provider client<br/>from Connection credentials"])
    schemas(["For each requested API<br/>Check export and process its schema"])
    counts(["Record boundAPIs and schema hashes<br/>Count instance conflict annotations"])
    missing{"Any APIs<br/>not exported?"}
    not_exported(["Set Ready=False<br/>with APINotExported"])
    waiting{"Waiting for an OpenAPI-source<br/>consumer CRD?"}
    related(["Sync selected related Secrets and ConfigMaps<br/>Remove copies that no longer match"])
    conditions(["Update Conflicts<br/>Set Synced=True and Ready=True"])
    status(["Persist changed status<br/>Requeue after 30 seconds"])
    stop(["Stop"])

    start --> get_binding
    get_binding --> exists
    exists -->|no| stop
    exists -->|yes| connection
    connection -->|no| pending
    connection -->|yes| provider
    provider --> schemas
    schemas --> counts
    counts --> missing
    missing -->|yes| not_exported
    missing -->|no| waiting
    waiting -->|yes| pending
    waiting -->|no| related
    related --> conditions
    pending --> status
    not_exported --> status
    conditions --> status
    status --> stop
```

For `CRD`, reconciliation pulls each exported provider schema with creation and updates controlled by `pullPolicy` and `updatePolicy`. For `OpenAPI`, it synthesizes a missing consumer CRD unless `pullPolicy: None`. An existing OpenAPI-source CRD is read, not refreshed by this controller.

A forbidden CRD pull sets `PermissionDenied=True` and `Ready=False`. Other API errors are returned for retry. Conflicts can coexist with `Ready=True`, and `Synced=True` describes setup, not proof that every instance has reached the provider. Instance synchronization runs separately.

## Deletion

```mermaid
flowchart TD
    start(["Deleting binding with cleanup finalizer"])
    provider(["Try to obtain provider credentials"])
    next_api{"More APIs<br/>to clean up?"}
    covered{"Another non-deleting binding<br/>lists this API?"}
    schema{"Consumer CRD provides<br/>a usable API version?"}
    drain(["Drain instances in scope<br/>Release their syncer finalizers"])
    cluster_binding{"ClusterBinding?"}
    remove_crd(["Delete consumer CRD"])
    related(["Delete related-resource copies<br/>owned by this binding"])
    release(["Remove binding cleanup finalizer"])
    stop(["Stop"])

    start --> provider
    provider --> next_api
    next_api -->|yes| covered
    covered -->|yes| next_api
    covered -->|no| schema
    schema -->|no| next_api
    schema -->|yes| drain
    drain --> cluster_binding
    cluster_binding -->|yes| remove_crd
    cluster_binding -->|no| next_api
    remove_crd --> next_api
    next_api -->|no| related
    related --> release
    release --> stop
```

Draining requests deletion only for owned provider copies, unless an instance uses `deletion-policy: Orphan`. Provider-client and provider-read failures can leave copies behind, but actual provider-delete errors can block cleanup. Binding cleanup does not wait for provider finalizers as normal instance deletion does.

The overlap check is API-wide, not a comparison of namespaces or Connections. A last ClusterBinding can delete the consumer CRD even when it was externally installed. A namespaced Binding keeps the shared CRD. Read the [cleanup limits](../../../usage/synchronization.md#delete-a-binding) before changing overlapping bindings or relying on automatic pruning.

Implementation: [binding reconcilers](https://github.com/kbind-dev/kbind/blob/v2-next/engine/binding/reconciler.go), [related resources](https://github.com/kbind-dev/kbind/blob/v2-next/engine/binding/related.go), and [cleanup](https://github.com/kbind-dev/kbind/blob/v2-next/engine/binding/cleanup.go).
