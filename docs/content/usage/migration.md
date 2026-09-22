# Moving from 0.x

v2 replaces the 0.x API model and binding protocol. **There is no automatic conversion or supported in-place upgrade of existing bindings.** Keep the [0.x documentation](https://docs.kbind.dev/latest/) for existing deployments, use separate test clusters to evaluate v2 first.

## Concepts that carry over

The consumer is still the source of desired state. The provider runs the service implementation. The konnector still sends spec to the provider and returns status to the consumer, connecting outward from the consumer.

The API objects and operational responsibilities around that loop have changed:

| 0.x concept | v2 replacement or change |
| --- | --- |
| `kube-bind.io` API group and its `v1alpha1`/`v1alpha2` kinds | `core.kbind.io/v1alpha1`, `catalog.kbind.io/v1alpha1`, and `iam.kbind.io/v1alpha1`, new objects, not a version-field edit |
| Consumer `APIServiceBinding` and connection bookkeeping | `Connection` references credentials, `ClusterBinding` or `Binding` chooses APIs and sync scope |
| `APIServiceExportTemplate` | Optional catalog `Export` |
| Legacy `Collection` | New catalog `Collection` grouping Exports, create a new manifest |
| `APIServiceExportRequest` / binding handshake | Optional HTTP `/api/bind` produces a one-apply core bundle |
| Per-consumer `APIServiceExport` | Provider opt-in labels and core discovery, optional `Grant` records service-layer issuance |
| `BoundSchema` / old schema intermediates | Core pulls CRDs or synthesizes them from OpenAPI, controlled by `Connection.spec.schema` |
| `APIServiceNamespace` / mapped provider namespaces | No equivalent in stock core, namespace and name stay unchanged |
| `Prefixed`, `Namespaced`, and `None` isolation modes | Identity mapping only in the shipped binary, no scope conversion |
| Permission claims and JSONPath reference following | Credential RBAC plus explicit `relatedResources` selectors for Secrets/ConfigMaps |
| Backend required for the binding protocol | Backend optional, directly apply the Secret, Connection, and bindings |

The new `ClusterBinding` shares a name with an old kind, not its group, schema, or meaning. Use fully qualified resource names when inspecting clusters that contain both API groups.

## Plan an explicit cutover

1. Inventory the APIs, objects, credentials, namespace mapping, related Secrets/ConfigMaps, and cleanup policies in your current installation. Back up both clusters' relevant resources and service data.
2. Choose a provider boundary that works with identity mapping. A 0.x object named with a consumer prefix or stored in a mapped namespace is not the same v2 target.
3. Install v2 on an isolated consumer. Use the [core quickstart](../setup/quickstart.md) to establish the new workflow. Keep the old konnector away from APIs and objects managed by the new one.
4. Recreate provider credentials and exported-API policy, then author new [core manifests](api-concepts.md). For the service layer, create [Exports and Collections](catalog.md) and obtain a new v2 bundle.
5. Recreate representative instances and validate schema behavior, spec ownership, status delivery, related resources, RBAC, and cleanup before planning a service-specific data migration.
6. Drain and retire old bindings using the cleanup behavior documented for their original release. Retain backups and credentials until the intended cleanup has completed.

Do not run both syncers against the same target objects as a migration shortcut. Do not switch `conflictPolicy` to `Adopt` just to bypass an unexplained collision: v2 adoption can take over an unowned target, not a target marked as owned by a different binding.

## Integration changes

The old cert-manager, Crossplane, CloudNativePG, and kro recipes are not v2 installation instructions. Their service operators may still run on a provider, but each offering needs a new Export or explicit core binding, v2-compatible schemas, matching names and scopes, and sufficient RBAC.

Review multi-version CRDs, conversion webhooks, built-in Kubernetes resource dependencies, and service-specific Secret references individually. v2's related-resource selectors do not follow arbitrary JSONPath references. The previous `Namespaced` isolation mode cannot be reproduced by changing a chart value or a `Mapper`.

See [synchronization](synchronization.md) for the shipped semantics and [architecture](../developers/architecture.md) for extension points.
