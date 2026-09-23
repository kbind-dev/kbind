# Troubleshooting

Start with the Connection, then the binding, then actual instances. A running Pod or `Synced=True` binding alone does not prove that every object was delivered.

Examples below use `consumer` and `provider` context names, replace them with yours. Do not include raw kubeconfigs, tokens, Secret contents, or unredacted connection details in bug reports.

## Collect the current state

```bash
kubectl --context=consumer get connections,clusterbindings
kubectl --context=consumer get bindings.core.kbind.io -A
kubectl --context=consumer get connection provider -o yaml
kubectl --context=consumer -n team-a get bindings.core.kbind.io widgets -o yaml
kubectl --context=consumer get crd widgets.example.org -o yaml
kubectl --context=consumer -n team-a describe widget my-widget
kubectl --context=consumer -n team-a get events --sort-by=.lastTimestamp
kubectl --context=consumer -n kbind logs \
  -l app.kubernetes.io/instance=konnector -c konnector --tail=200
```

For the [Quickstart](../setup/quickstart.md), substitute Connection `demo-provider`, ClusterBinding `widgets`, and namespace `default`, logs are in the host konnector terminal.

Record the image tag or source commit, Kubernetes versions, schema source/policies, condition reasons/messages, and a redacted minimal reproducer.

## Connection does not become Ready

| Symptom | What to check |
| --- | --- |
| `SecretValid=False`, reason `SecretNotFound` | The referenced Secret namespace/name/key, nonempty kubeconfig data, and parse errors in the condition message. The same reason is used for malformed or unusable kubeconfig, not only missing Secrets. |
| `Connected=False`, reason `Pending` | Provider endpoint reachability, TLS CA/server name, credential validity, and identity-read errors. |
| `PermissionDenied=True`, reason `Forbidden` | Provider identity/discovery RBAC. Test with the same identity as the Secret, not your administrator context. |
| `ClusterIdentityChanged` | The Secret now points to a different provider UID. Restore the original endpoint/credentials, or use a new Connection with deliberate migration. |
| `DrainingBindings` | The Connection is deleting but explicit referencing bindings still exist. Finish their cleanup first. |

The API requires an explicit namespace in `kubeconfigSecretRef`, it does not infer the konnector namespace. An omitted key defaults to `kubeconfig`.

To confirm a Secret exists without exposing its data:

```bash
kubectl --context=consumer -n kbind get secret provider-kubeconfig \
  -o custom-columns=NAME:.metadata.name,TYPE:.type
```

A workstation kubeconfig using a local exec plugin or file paths may be unusable in the stock image. A short-lived bearer token can expire, the core does not renew it.

For Pod deployments, verify the provider URL is reachable from the consumer network. `https://127.0.0.1:...` refers to the consumer Pod itself, not your workstation or a kind provider node. Fix the endpoint and certificate configuration instead of disabling TLS verification.

`SchemaInSync` is defined in the API but not currently emitted by the Connection reconciler. Do not wait on that condition.

## An API is missing or the binding says APINotExported

For `schema.source: CRD`:

```bash
kubectl --context=provider get crd widgets.example.org --show-labels
kubectl --context=provider get crds -l core.kbind.io/exported=true
kubectl --context=consumer get connection provider \
  -o jsonpath='{.status.activeSchemaSource}{"\n"}{.status.exportedAPIs}{"\n"}'
```

The provider CRD must be labelled `core.kbind.io/exported: "true"`. The binding API name must be the full plural/group name, such as `widgets.example.org`, not `Widget` or `example.org/v1`.

Allow about 30 seconds for discovery. If provider credentials cannot list CRDs, `Auto` currently returns that error, it does not fall back on Forbidden. Use `OpenAPI` explicitly for a CRD-less provider. If an empty label selection should export nothing, use explicit `CRD`, because Auto can fall through to OpenAPI when a successful CRD list contains no exports.

For `OpenAPI`, the provider must expose usable discovery and `/openapi/v3` schemas. Kubernetes built-in groups are excluded. Missing documents or unusable schemas can cause individual resources to be skipped. The label filter does not apply to this source.

Removing an export label is not a credential revocation or guaranteed cleanup operation. Revoke access using provider authorization and unbind explicitly when retiring an API.

## Binding is Ready but instances do not sync

Check all of the following:

1. The consumer CRD exists and is Established.
2. It has `core.kbind.io/managed: "true"` and `core.kbind.io/connection` naming the intended Connection.
3. That Connection is Ready and has nonempty identity UIDs.
4. A Ready binding covers the instance's namespace. A namespaced Binding cannot activate a cluster-scoped API.
5. No overlapping ClusterBinding takes precedence over the intended Binding.
6. Provider and consumer credentials have the required cluster-wide list/watch access.
7. Provider create/patch and namespace-create operations are allowed.
8. There is no foreign provider object or provider admission rejection.

Read the ownership/conflict markers:

```bash
kubectl --context=consumer -n team-a get widget my-widget \
  -o jsonpath='{.metadata.annotations.core\.kbind\.io/conflict}{"\n"}'
kubectl --context=provider -n team-a get widget my-widget -o yaml
```

`Synced=True` reports binding setup, not a per-instance acknowledgment. In particular, with CRD source and `pullPolicy: None`, a binding can currently report success even if the externally managed consumer CRD is absent. With OpenAPI source and `None`, a preinstalled CRD also needs management markers supplied by you.

Instances keep their namespace/name. A kubeconfig context's namespace only sets the heartbeat namespace, it does not re-home objects. A provider credential scoped to a different namespace is not sufficient.

Provider-side per-instance failures may appear as Warning Events or logs even while binding `PermissionDenied=False`. A namespaced Binding does not make the current dynamic informers namespace-scoped.

After rotating a valid kubeconfig, an existing engaged client may still use its previous credentials. Restart the konnector and recheck actual delivery. With a chart deployment, use the deployment name from:

```bash
kubectl --context=consumer -n kbind get deployment \
  -l app.kubernetes.io/instance=konnector
```

Then run `kubectl --context=consumer -n kbind rollout restart deployment/<name>` and wait for the rollout.

## Conflicts

`ForeignObjectExists` means the provider target has no consumer ownership markers. `OwnedByAnother` means its markers do not match this consumer cluster and object UID.

Inspect the provider target and determine which system should own it. If intentional, `conflictPolicy: Adopt` can take a markerless target, but may overwrite its fields. It never steals another marked object's ownership. Do not remove ownership annotations simply to force adoption.

The consumer conflict annotation and Events are more reliable than a custom resource's `status.conditions`, which its schema may prune. Binding conflict counts normally refresh every 30 seconds and cover instance conflicts only.

## Schema or status differs from the provider

CRD-source pulls retain one version and remove conversion webhooks. OpenAPI synthesis does not faithfully reproduce all validation/defaulting, referenced schemas, conversion behavior, printer columns, or subresources. Neither path installs the provider's admission webhooks or controllers.

Check:

- The consumer CRD storage/served version and its status subresource/schema.
- Whether `updatePolicy: Once` intentionally pins a schema.
- Whether `pullPolicy: None` leaves updates to your external CRD manager.
- Whether you are using `OpenAPI + Bound`, which currently does not refresh an already-installed CRD under `Always`.
- Provider admission errors: local schema acceptance does not prove the provider accepts a spec.
- Whether the provider object has status at all. The demo has no real Widget controller, the quickstart patches status manually.

The engine copies only bound instance `spec` up and `status` down, not arbitrary labels, annotations, or top-level payload fields. See [schema fidelity](api-concepts.md#schema-updates-and-fidelity).

## Related resources do not appear

Check the declared direction and source namespace. `FromProvider` means create the source on the provider, not on the consumer. The full `config/samples/widget.yaml` contains local Secret/ConfigMap manifests as well as a Widget, applying it on the consumer does not demonstrate provider-to-consumer copying.

Check selectors, source/target RBAC, and target ownership. A foreign Secret/ConfigMap is silently left untouched, `Adopt` and binding instance conflict counts do not apply to these collisions. Source labels are not copied to the target.

Wait for the binding's periodic reconciliation, normally 30 seconds. An omitted selector matches every object in scope. Avoid multiple selectors represented as separate entries for the same resource/direction: their garbage collection can conflict. Removing a whole related-resource entry can leave its prior copies behind.

If you narrowed chart `rbac.boundResourceGroups`, add the necessary related Secret/ConfigMap permissions explicitly. The fixed credential-Secret rule alone does not allow creation/deletion of synced Secret copies.

## No heartbeat Lease

Look in the provider kubeconfig context namespace, or `kbind` if unset:

```bash
kubectl --context=provider get leases -A -l core.kbind.io/managed=true
```

Check Lease read/create/update and namespace permissions. Heartbeat errors do not make a healthy sync fail, the engine logs them at verbosity 2. `/readyz` is also only a process probe, not a heartbeat check.

Core does not delete stale Leases or reap disconnected consumers. Those actions belong to the optional backend or your operations.

## Deletion is stuck

Keep the konnector running. Inspect `metadata.deletionTimestamp`, `metadata.finalizers`, Events, and controller logs on:

- The consumer instance (`core.kbind.io/syncer`).
- Its provider copy, including provider-controller finalizers.
- The Binding/ClusterBinding (`core.kbind.io/cleanup`).
- The Connection and referenced Secret (`core.kbind.io/cleanup`).

Normal instance deletion waits for the owned provider object to be gone. Restore provider access or resolve the provider controller's cleanup failure. If retaining the provider object is intentional, the [Orphan policy](synchronization.md#delete-one-consumer-instance) releases the consumer sync finalizer once the engine can reconcile it.

A Connection waits for all explicit bindings referencing it, deleting only the Connection will not delete those bindings. Delete bindings first and let them finish, then the Connection, then its Secret. Stop the controller or uninstall Helm only afterward.

Binding teardown is best-effort in some provider-read/credential failure cases, so a completed delete is not proof that every remote copy disappeared. Check the provider after cleanup. Overlapping bindings and in-place scope edits can also leave old copies/finalizers behind, see [cleanup limits](synchronization.md#delete-a-binding).

Do not blindly strip finalizers or delete CRDs to unblock an operation. This bypasses ownership-aware cleanup and can lose consumer data or abandon provider resources. If the controller cannot be recovered, back up the affected resources, verify ownership and remote cleanup manually, and treat any finalizer removal as an explicit disaster-recovery action.
