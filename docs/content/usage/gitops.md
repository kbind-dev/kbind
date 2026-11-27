# GitOps

The v2 core accepts ordinary Kubernetes YAML. You can manage a Connection and its explicit bindings with your existing GitOps controller, no browser onboarding or continuously running CLI is required.

## Try the core without a backend

This local example uses the existing demo samples to supply credentials, a Connection,
and a ClusterBinding directly. It needs no backend, catalog, login, or `kubectl bind`
plugin. Use a v2 source checkout, Go matching `go.mod`, Docker, kind, and kubectl.
Run commands from the repository root.

The demo creates or reuses kind clusters named `kbind-provider` and `kbind-consumer`.
Use fresh, disposable clusters with those names: the script applies sample resources
and replaces its credential Secret. It writes administrator kubeconfigs under `/tmp`
and copies the provider kubeconfig into a consumer Secret. These credentials are for
this local demo only; use restricted provider credentials and secure Secret delivery
for a real deployment.

### Prepare the two clusters

```sh
make konnector
make demo
```

The script installs the exported Widget CRD and related Secret/ConfigMap on the provider.
On the consumer it installs API definitions, creates the credential Secret, and applies
`config/samples/binding.yaml`. That file connects `demo-provider` to the Secret and
selects `widgets.example.org` through ClusterBinding `widgets`.

In a second terminal at the repository root, start the konnector and leave it running:

```sh
KUBECONFIG=/tmp/kbind-consumer.kubeconfig ./bin/konnector
```

These kubeconfigs use host-mapped endpoints, so this example runs the konnector on your
workstation. They are not suitable for a konnector Pod in another cluster.

### Verify synchronization

In the first terminal, wait for readiness before creating a Widget:

```sh
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig wait \
  --for=condition=Ready connection/demo-provider clusterbinding/widgets --timeout=120s
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig wait \
  --for=condition=Established crd/widgets.example.org --timeout=120s
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig apply -f config/samples/widget.yaml
kubectl --kubeconfig /tmp/kbind-provider.kubeconfig -n default wait \
  --for=create widget/my-widget --timeout=120s
kubectl --kubeconfig /tmp/kbind-provider.kubeconfig -n default wait \
  --for=jsonpath='{.spec.size}'=large widget/my-widget --timeout=120s
```

There is no Widget service controller in this demo. Simulate its status update on the
provider and verify that it reaches the consumer:

```sh
kubectl --kubeconfig /tmp/kbind-provider.kubeconfig -n default patch widget my-widget \
  --subresource=status --type=merge -p '{"status":{"phase":"Running"}}'
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig -n default wait \
  --for=jsonpath='{.status.phase}'=Running widget/my-widget --timeout=120s
```

The binding also copies the provider's related Secret and ConfigMap. Check their
ownership markers without printing Secret data:

```sh
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig -n default wait \
  --for=create secret/widgets-connection configmap/widgets-tuning --timeout=120s
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig -n default get \
  secret/widgets-connection configmap/widgets-tuning \
  -o 'custom-columns=NAME:.metadata.name,BINDING:.metadata.annotations.core\.kbind\.io/related-binding'
kubectl --kubeconfig /tmp/kbind-consumer.kubeconfig get clusterbinding widgets \
  -o jsonpath='{.metadata.uid}{"\n"}'
```

Both `BINDING` values should match the ClusterBinding UID. The consumer sample creates
only the Widget; the konnector creates these related copies.

### Clean up

Stop the host konnector with Ctrl-C, then remove only the disposable demo clusters and
their credential files:

```sh
kind delete cluster --name kbind-consumer
kind delete cluster --name kbind-provider
rm /tmp/kbind-consumer.kubeconfig /tmp/kbind-provider.kubeconfig
```

For an existing deployment, follow [Pruning is unbinding](#pruning-is-unbinding) instead
of deleting clusters. To manage the same core inputs declaratively, continue below.

## Separate definitions, credentials, and generated state

Manage these layers deliberately:

1. Install the three core CRDs and the konnector.
2. Create application/credential namespaces and deliver provider credentials securely.
3. Apply Connections and explicit Bindings/ClusterBindings.
4. Wait for the bound consumer CRDs to become Established before applying their instances.

Connections and bindings tolerate arriving before their dependencies, but Kubernetes rejects unknown kinds before a CRD exists. Use your GitOps tool's existing dependency/wave/health mechanisms rather than relying on YAML document order.

Keep these out of Git:

- Plaintext provider kubeconfigs and base64-only Secret manifests containing credentials.
- Controller-written `status`, UIDs, resource versions, finalizers, and managed fields.
- Generated autoBind ClusterBindings, unless you instead turn autoBind off and manage explicit bindings.
- Consumer copies of `FromProvider` related resources.

Use an existing encrypted-Secret or external-secret workflow for credentials. The resulting Kubernetes Secret must have the namespace, name, and data key referenced by the Connection.

## Obtain a bundle from a provider

With the [CLI](../setup/kubectl-plugin.md) installed and logged in:

```sh
kubectl bind export widgets -o yaml > widgets-bundle.yaml
```

This provisions a real Grant and redeems a one-time pickup URL, it is not a dry run. YAML is the only supported output format. This mode does not read the consumer kubeconfig, install the konnector, create namespaces, or apply resources, even when `--install-konnector` is true.

Install the core CRDs, konnector, and `kbind` namespace separately. The bundle contains a long-lived provider token in plaintext YAML. Encrypt its Secret or use a secret manager before committing anything.

Pickup expiry does not expire a saved bundle's credentials. The provider's optional reaper can revoke Grants that have not yet heartbeated, so coordinate its TTL with rollout timing. See [bundle lifetimes](catalog.md#three-different-lifetimes).

## An explicit application binding

After creating `team-a` and delivering `kbind/provider-kubeconfig`:

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
  autoBind: false
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

A namespaced Binding is often preferable when only one application's namespace should sync. It is not a namespaced CRD or a replacement for provider/consumer RBAC.

Use an explicit API allowlist rather than `autoBind: true` if newly exported provider APIs require review. Use `source: CRD` when provider labels must remain the opt-in boundary, Auto can fall through to OpenAPI when no labelled CRDs are found.

## Choose one schema owner

### Konnector-managed schemas

With `pullPolicy: Bound` or `All`, avoid also reconciling the same consumer CRD spec from Git. `updatePolicy: Always` can overwrite competing changes with the provider schema.

`Once` stops subsequent schema spec updates, but is not a version selector or approval queue. Test compatibility before changing policies, and note the [OpenAPI update limitation](api-concepts.md#schema-updates-and-fidelity).

### Externally managed schemas

Use `pullPolicy: None` when Git owns the consumer CRD spec. Supply the correct group, resource, scope, usable storage version, status schema/subresource, and compatible validation yourself.

For OpenAPI-source Connections, include these markers in the consumer CRD:

```yaml
metadata:
  labels:
    core.kbind.io/managed: "true"
  annotations:
    core.kbind.io/connection: provider
```

For CRD-source Connections, the engine stamps these markers on an existing CRD. Configure your GitOps tool not to fight those metadata changes.

**Important:** `None` means “do not install the schema,” not “never manage or delete this CRD.” Last-ClusterBinding cleanup currently deletes the referenced CRD even under `None`. Prefer a carefully controlled Binding lifecycle, disable destructive automatic pruning for shared resources, and read [unbinding behavior](synchronization.md#delete-a-binding).

Likewise, core CRDs included by the Helm chart are ordinary release templates. If Git owns those three definitions, use `installCRDs: false` from the initial Helm install, do not hand their lifecycle back and forth casually.

## Drift and ownership

For bound instances, Git should normally manage consumer `spec`, not provider copies or consumer `status`. The konnector force-applies its fields to owned provider copies, so a second provider-side GitOps manager for those same fields will compete with it.

An existing foreign provider object is not adopted by default. `conflictPolicy: Adopt` is an explicit and potentially destructive decision for a markerless object, not a generic way to suppress drift. It does not steal objects marked for another consumer.

Avoid managing the same API through multiple overlapping bindings or different Connections. The current resolver is not a multi-provider routing system. Also avoid editing a binding's API list or Connection reference as though it performed an atomic migration: cleanup operates on deletion and the current spec, not an inventory of all previous specs.

## Rotate without replacing identity

Keep `kubeconfigSecretRef` stable and update the Secret's data for the same provider cluster. The reference and pinned identities are immutable.

After delivering new valid credentials, restart the konnector to rebuild engaged instance clients, then verify spec/status round-tripping before revoking the old credentials. Secret updates alone do not guarantee hot rotation of an already-engaged client.

A different provider identity requires a new Connection and an explicit migration. Do not try to overwrite `remoteClusterUID`.

## Pruning is unbinding

Removing a binding from Git and allowing it to be pruned invokes the same destructive cleanup as `kubectl delete`. Removing a last ClusterBinding can delete the consumer CRD and every instance of that API.

For a planned removal:

1. Pause automatic recreation/pruning while coordinating the change.
2. Back up relevant consumer resources and annotate instances with `core.kbind.io/deletion-policy: Orphan` if provider copies must remain.
3. Delete and verify instances or bindings while the controller and credentials work.
4. Delete Connections after explicit bindings have drained.
5. Remove credential Secrets, then the controller/CRDs if no longer needed.

Do not prune the credentials, namespace, CRDs, and controller first. Cleanup finalizers preserve ordering in common cases, but they are not a distributed rollback mechanism. See [Troubleshooting](troubleshooting.md) for incomplete teardown.
