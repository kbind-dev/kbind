# Catalog, issuance, and inventories

The optional backend turns exported provider APIs into a browsable catalog and issues the credentials needed to bind them. All service-layer resources below live on the **provider** and are cluster-scoped:

| Resource | API | Purpose |
| --- | --- | --- |
| `Export` | `catalog.kbind.io/v1alpha1` | Describe an offering, its APIs, and binding defaults |
| `Collection` | `catalog.kbind.io/v1alpha1` | Group Exports for browsing |
| `Grant` | `iam.kbind.io/v1alpha1` | Record an identity's issued API access and credential artifacts |

These are not consumer sync instructions. The konnector consumes the core Secret, Connection, and binding objects produced at the end of issuance. See [API concepts](api-concepts.md), the [catalog API reference](../reference/crd/index.md#catalogkbindiov1alpha1), and the [IAM API reference](../reference/crd/index.md#iamkbindiov1alpha1).

## Publish an offering

Start with a running [backend](../developers/backend/index.md) and a provider API that actually implements your service. In this checkout the sample API is **`widgets.example.org`**, not `widgets.example.com`.

From the repository root:

```sh
kubectl --context=provider apply -f config/samples/provider-widget-crd.yaml
```

The sample CRD already has `core.kbind.io/exported: "true"`. For an existing CRD:

```sh
kubectl --context=provider label crd widgets.example.org \
  core.kbind.io/exported=true --overwrite
```

The label is the core's export declaration. Creating a catalog Export does not label a CRD, install it, or start a controller that implements it.

Save the following as `widgets-catalog.yaml`:

```yaml
apiVersion: catalog.kbind.io/v1alpha1
kind: Export
metadata:
  name: widgets
spec:
  title: Widgets
  description: A demonstration API for managed widgets.
  docs: https://example.org/widgets
  apis:
    - name: widgets.example.org
  defaults:
    conflictPolicy: Fail
    relatedResources:
      - group: ""
        resource: configmaps
        direction: FromProvider
        selector:
          labelSelector:
            matchLabels:
              widgets.example.org/shared: "true"
---
apiVersion: catalog.kbind.io/v1alpha1
kind: Collection
metadata:
  name: examples
spec:
  title: Example services
  description: APIs for learning the binding workflow.
  exports:
    - name: widgets
```

```sh
kubectl --context=provider apply -f widgets-catalog.yaml
kubectl --context=provider wait --for=condition=Ready \
  exports.catalog.kbind.io/widgets --timeout=60s
kubectl --context=provider get exports.catalog.kbind.io,collections.catalog.kbind.io
```

The issuer module's Export controller verifies that **every named CRD** exists with the export label. Missing or unexported APIs produce `Ready=False` with reason `APINotExported`. The gateway hides non-ready Exports and refuses to bind them. This readiness condition does not check service-controller health, API-specific business logic, or an individual consumer's connectivity.

Collections are presentation only. Their `exports` entries refer to Export names, not CRD names. They cannot be bound as a unit, and do not grant access. The HTTP catalog includes only their ready members, and omits Collections with no visible members. There is no Collection readiness controller.

### Related resources

The example offering declares provider-to-consumer ConfigMap sync. Create an object that matches its label:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: widget-demo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: widget-settings
  namespace: widget-demo
  labels:
    widgets.example.org/shared: "true"
data:
  endpoint: https://widgets.example.org
```

Apply this YAML on the provider. With the default cluster-wide bundle and compatible credentials, the related ConfigMap keeps its name and namespace on the consumer. It is not a JSONPath-followed reference from a Widget.

Rules support only `secrets` and `configmaps` in the core API group, with `FromProvider` or `FromConsumer` direction. Select by Kubernetes `labelSelector`, exact `names`, or both. There is no reference-following selector. An omitted selector is broad, use explicit selectors, especially for Secrets.

For example, an additional rule can send selected consumer credentials to the provider:

```yaml
group: ""
resource: secrets
direction: FromConsumer
selector:
  labelSelector:
    matchLabels:
      widgets.example.org/input: "true"
  names:
    - widget-input
```

This fragment belongs under `spec.defaults.relatedResources`. It is an explicit authorization and data-flow decision, not an automatic dependency discovery feature. Do not add it unless those consumer Secrets should leave their cluster.

Related-resource selectors control sync, not issued RBAC. In default `issuerScope: Cluster`, a `FromProvider` Secret rule grants Secret reads across provider namespaces, not only the selected Secrets. A `FromConsumer` rule adds write permissions too. Review [credential scope](../developers/backend/index.md#credential-scope-and-installer-privileges) before publishing secret-bearing offerings.

### How defaults propagate

The gateway copies an Export's `spec.apis`, `spec.defaults.conflictPolicy`, and `spec.defaults.relatedResources` into the Grant. Bundle assembly copies those fields from the Grant into a generated **`ClusterBinding`**.

The Grant is a resolved record, not a live reference to Export defaults. Editing an Export alone does not change existing Grants or consumer bindings. Binding the same Export again as the same identity updates that Grant's spec from the current Export and returns a new bundle. Apply that bundle to update the consumer.

`Fail` is the core's default conflict policy. `Adopt` can take over an unowned object, but does not authorize stealing another binding's managed object. The catalog has no defaults for namespace remapping, arbitrary object templates, or a namespaced `Binding` output, those are not gateway bind options.

## Browse and bind

Using the [v2 CLI](../setup/kubectl-plugin.md):

```sh
kubectl bind login https://bind.example.com
kubectl bind catalog
kubectl bind export widgets \
  --kubeconfig=./consumer.kubeconfig \
  --install-konnector=false
```

This example assumes the konnector and core CRDs are already installed. `export` otherwise defaults to installing/upgrading the konnector, choose a matching image explicitly if you use that behavior.

In the browser:

1. Open the gateway root, sign in, and choose **Catalog**.
2. Inspect the offering's API names, description, documentation link, and related-resource direction/selector hints.
3. Click **Bind**. The gateway provisions a Grant and opens a one-time bundle dialog.
4. Choose **Download bundle** or use the displayed `curl | kubectl apply` command once. Downloading and then attempting the same pickup URL again will fail, apply the downloaded file instead.

Install the konnector before applying a downloaded bundle. **Clusters → Connect a cluster** provides the install command and manifest download. This does not establish a provider Connection by itself.

The dialog also has **Copy kbind command**. That command requires its own CLI login and makes a fresh bind request, it does not redeem the dialog's existing ticket. If browser apply is enabled by the operator, the UI additionally allows you to paste a consumer kubeconfig for installation or bind-and-apply. This sends consumer credentials through the gateway. Prefer local CLI or reviewed manifest apply when that trust is not appropriate.

The browser does not edit Exports/Collections, delete Grants, choose an existing cluster as an apply target, or create service instances. Use Kubernetes tools for those operations. An inventory entry is not a stored consumer credential.

### CLI sessions and cluster selection

`kubectl bind login` opens the gateway's OIDC login URL and listens for the browser's callback on a random localhost port. Login times out after five minutes. `--no-browser` prints the URL instead, but the browser must still reach that localhost listener. It is not a device-code login.

The last successful login selects the default gateway. Override it with `kubectl bind catalog --server=https://other-bind.example.com`. The URL is a gateway, not a Kubernetes API server. Use an explicit `http://` scheme for local development.

The CLI keeps bearer credentials in `kbind/config.json` under Go's `os.UserConfigDir()`:

| Platform | Usual location |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/kbind/config.json`, or `~/.config/kbind/config.json` |
| macOS | `~/Library/Application Support/kbind/config.json` |
| Windows | `%AppData%\kbind\config.json` |

New files use mode `0600` where supported. This is not an encrypted credential vault, protect existing file permissions too. There is no CLI logout command. Removing the cached token forgets it locally but does not revoke credentials, and browser sign-out does not invalidate a copied session token. The CLI has no Kubernetes-token login mode, though the backend can support [TokenReview authentication](../developers/backend/index.md#kubernetes-tokenreview-authentication).

Consumer selection follows client-go loading: `--kubeconfig`, then `KUBECONFIG`, then the normal kubeconfig and its current context. There are no `--context` or `--namespace` overrides. Use a dedicated consumer kubeconfig when managing several clusters.

### Installing through the CLI

To install without Helm, choose a built v2 image available to your cluster:

```sh
kubectl bind connect --kubeconfig=./consumer.kubeconfig \
  --konnector-image=registry.example.com/platform/konnector:v2-example
kubectl --kubeconfig=./consumer.kubeconfig -n kbind \
  rollout status deployment/konnector
```

Replace the illustrative image. The compiled `latest` default is not a v2 guarantee. `connect` installs locally embedded manifests without contacting the gateway or requiring login. It has no print-only or dry-run mode. It does not register a consumer in the provider inventory, that requires a bound Connection to start heartbeating.

`export` also installs by default. Use `--install-konnector=false` for an existing Helm/GitOps-managed konnector, or pass `--konnector-image` to select the image explicitly. Installation and bundle apply use server-side apply with forced field ownership and can overwrite another manager's fields. They are not transactional, failure can leave earlier objects installed. Review the [installer permissions](../developers/konnector/index.md#rbac-and-credentials).

The public gateway endpoint `/api/konnector` serves a separate installation manifest using the backend's `konnectorImage` setting. Download and inspect it before applying. Its image can differ from the CLI's.

## Binding and bundle pickup

The gateway flow is:

1. Authenticate the caller and require a ready Export.
2. Create or update one Grant for `(Export name, identity subject)`.
3. Wait up to 30 seconds for the issuer's `Ready=True`.
4. Return a one-time pickup URL and its expiration.
5. Redeem that URL for the consumer bundle.

There are no bind-request CRDs, request phases, approval queues, or browser device-code polling in this protocol.

The Grant name is `<export>-<identity-hash>`, where the hash is the first ten hexadecimal characters of SHA-256 of the identity subject. The provider boundary namespace is `kbind-<identity-hash>` and is shared by the identity's Grants. OIDC subjects are `<issuer-url>#<configured-username-claim-value>`, TokenReview subjects are `kubernetes#<username>`.

The issuer creates a ServiceAccount and token Secret per Grant, resource-specific RBAC, and a Lease permission home in the boundary namespace. The token Secret is populated by Kubernetes' ServiceAccount token controller. `Grant.status` reports `namespace`, `serviceAccount`, `tokenSecret`, and conditions.

The bundle always contains:

| Object | Name / namespace | Purpose |
| --- | --- | --- |
| `v1/Secret` | Grant name, namespace `kbind` | `stringData.kubeconfig` with the provider token, endpoint, CA, and boundary context namespace |
| `core.kbind.io/v1alpha1/Connection` | Grant name, cluster-scoped | Reference the kubeconfig Secret |
| `core.kbind.io/v1alpha1/ClusterBinding` | Grant name, cluster-scoped | Select the Grant's APIs and resolved defaults |

It does not include the core CRDs, konnector Deployment, `kbind` namespace, or service instances. CLI install/apply can supply installation and namespace prerequisites, plain `kubectl apply` of the bundle cannot.

### Three different lifetimes

| Item | Default lifetime | What expiry/revocation means |
| --- | --- | --- |
| Browser/CLI session | 12 hours | Log in again to call authenticated gateway APIs |
| Pickup URL | 5 minutes, one successful consumption | Bind again for a new pickup URL |
| Issued provider credential | Long-lived Secret-based SA token | Remains usable independently of session/pickup expiry until revoked or otherwise invalidated |

The one-time token is stored on the Grant as a hash and expiry, not gateway memory. Optimistic concurrency enforces single consumption across replicas. A new bind replaces any outstanding pickup for the same Grant. A used, expired, or unknown token returns HTTP `410`.

Pickup consumes the token **before** assembling the bundle. If the response fails, is lost, or credential assembly fails after consumption, bind again, there is no retryable download ticket. Protect pickup URLs like passwords: possession is sufficient to retrieve the credentials without a session.

`kubectl bind export widgets -o yaml` is the GitOps path. It still provisions and picks up a real Grant, but does not apply to the consumer. Secure the credential material before storing it in Git. See [GitOps](gitops.md).

### Direct Grant management

Normally the gateway creates Grants. Trusted provider automation can also create them through Kubernetes:

```yaml
apiVersion: iam.kbind.io/v1alpha1
kind: Grant
metadata:
  name: widgets-automation
spec:
  identity:
    subject: https://sso.example.com#automation
    displayName: Widget automation
  exportName: widgets
  apis:
    - name: widgets.example.org
  conflictPolicy: Fail
  relatedResources:
    - group: ""
      resource: configmaps
      direction: FromProvider
      selector:
        labelSelector:
          matchLabels:
            widgets.example.org/shared: "true"
```

The issuer provisions from this spec. It does not resolve `exportName` back to an Export, validate caller entitlement, or mint a pickup URL simply because a Grant exists. Direct Grant writers are trusted credential administrators. The illustrative name above is not the gateway's deterministic name, binding through the gateway would use its own record. For a complete gateway-issued bundle, use the bind API rather than hand-authoring Grant status or pickup annotations.

## Revoke and retire an offering

Inspect provider records:

```sh
kubectl --context=provider get grants.iam.kbind.io
kubectl --context=provider get grants.iam.kbind.io GRANT_NAME -o yaml
```

Revoke a specific Grant:

```sh
kubectl --context=provider delete grants.iam.kbind.io GRANT_NAME
```

The issuer's `iam.kbind.io/cleanup` finalizer removes that Grant's ServiceAccount, token Secret, ClusterRole/ClusterRoleBinding, and Role/RoleBinding. The issuer must be running for finalizer cleanup to complete. Do not remove the finalizer as a substitute for credential revocation.

Ordinary revocation keeps the provider boundary namespace and synced objects. It also does not remove consumer Secrets, Connections, bindings, or CRDs. Consumers should deliberately disengage through their [core lifecycle](api-concepts.md) rather than assume provider credential revocation cleaned up their cluster.

Deleting an Export removes it from catalog/bind lookup but does not delete its existing Grants or revoke their tokens. Removing a CRD's export label makes the catalog Export non-ready after reconciliation, it is not a credential revocation operation either. Revoke outstanding Grants explicitly when retiring access.

Deleting a Grant is not a permanent deny-list entry: an authenticated caller can bind an offering again while it remains available. Plan gateway access and offering withdrawal as well as credential cleanup.

### Reaper policy

The reaper is disabled by default. With it enabled:

- `reaper.ttl` / `--reaper-ttl` defaults to **30 minutes**.
- `reaper.interval` / `--reaper-interval` defaults to **1 minute**.
- `reaper.revoke` defaults to `false`: mark `Stale=True` and add `iam.kbind.io/stale-since`, but retain credentials.
- `reaper.deleteBoundary` defaults to `false` and only has an effect when revocation is enabled.

It reads managed heartbeat Leases in the Grant's boundary namespace. Matching uses the Lease's `core.kbind.io/connection` annotation and the generated Connection/Grant name. If matching Leases exist, the newest matching renewal determines staleness (`LeaseExpired`). Without a matching Lease, another fresh Lease in the same boundary conservatively keeps the Grant alive, accommodating renamed Connections. A fully silent boundary becomes stale. If no renewed Lease exists, the reaper waits one TTL after the Grant's Ready transition (`NoLease`). Fresh heartbeats clear prior stale markings.

With revocation enabled, the reaper deletes stale Grants and the issuer finalizer revokes their credentials. With boundary deletion also enabled, it can delete the namespace and everything in it, but skips deletion while another non-deleting Grant shares that identity.

This is **not** complete cleanup of a consumer's synced objects. Default cluster-scoped credentials reproduce consumer namespace names, many provider objects can therefore live outside the credential boundary. Boundary deletion does not delete those namespaces or cluster-scoped objects.

Coordinate destructive policy with outages, paused consumers, and GitOps rollouts. A downloaded-but-not-applied bundle may be revoked as never connected. The reaper's TTL is independent of the inventory's Live/Stale display threshold.

## Cluster and instance inventories

The browser's **Clusters** view and `kubectl bind clusters` use `GET /api/clusters`. There is no cluster registration resource or browser `/clusters` page route, the browser switches views within the app.

The inventory correlates Grants with managed Leases using the Connection name and consumer cluster UID annotations. It reports:

- Consumer UID, latest heartbeat, and whether any binding is live.
- Per-binding Grant, Export, identity, boundary namespace, and heartbeat.
- Best-effort counts of managed provider objects per API and consumer UID.
- Pending Grants without a usable matching Lease (`NeverConnected` in the CLI).

“Live” means a renewal within twice `leaseDurationSeconds`, a missing duration uses 60 seconds, hence a 120-second grace window. It is not a guarantee that every resource is syncing correctly. “NeverConnected” means the inventory found no matching Lease, not proof the bundle was never applied: renamed Connections, deleted Leases, or missing annotations can also prevent correlation.

`GET /api/catalog/{export}/instances`, the browser's **Synced instances** expander, and `kubectl bind instances [export]` show **provider-side** object metadata, not service health or a remote consumer object browser:

- Bound-API rows are objects carrying the core managed label, with consumer UID and identity attribution when the Lease/Grant link is available.
- Related `FromConsumer` rows are managed provider copies marked as related resources, the listing applies name restrictions, but is not a strict per-Grant or label-selector-filtered inventory.
- Related `FromProvider` rows are provider originals matching the declared label selector (and names if supplied). Without a `labelSelector`, these originals are omitted even if a names-only sync rule is valid. Consumer-side copies are not inspected. Secret payloads are not returned by this endpoint.

Counts and instance lists are best effort. Unreadable/unavailable APIs can yield zero counts or omitted rows instead of failing the entire view. Multiple Exports sharing an API can show the same objects, the inventory is not accounting by individual Grant. It also follows the current Export definition, not every historical Grant's defaults.

All authenticated gateway callers can read these inventories, they are not filtered to the caller's identity. If results are unexpected, inspect provider Grants and Leases and follow [troubleshooting](troubleshooting.md).
