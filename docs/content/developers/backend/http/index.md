# HTTP API

This reference describes the implemented `cmd/backend` binary and gateway protocol. See [deployment](../index.md) for Helm, OIDC, HTTPS, and RBAC configuration, and [catalog usage](../../../usage/catalog.md) for the lifecycle.

## Authentication conventions

**Authenticated** in the tables means either:

- A `kbind_session` cookie issued by the gateway's OIDC login flow.
- `Authorization: Bearer <kbind-session-token>` containing that same signed, encrypted session token.
- When `--kubernetes-auth=true`, a provider-cluster bearer token successfully verified through Kubernetes TokenReview.

An arbitrary OIDC ID/access token is not a gateway session token. The Kubernetes authenticator is additive, the binary still configures OIDC when the gateway is enabled. Bearer extraction takes precedence over the session cookie.

Authenticated APIs do not implement per-Export entitlements or caller-filtered inventories. OIDC groups and TokenReview groups are recorded, not used to authorize catalog operations.

### Read and inventory endpoints

| Method | Path | Authentication | Response and semantics |
| --- | --- | --- | --- |
| `GET` | `/api/provider` | None | `{name, version?, authMethods, applyEnabled}`. Binary authenticator names are `oidc` and optionally `kubernetes`. |
| `GET` | `/api/me` | Authenticated | `{subject, displayName?, groups?}` for the caller. |
| `GET` | `/api/catalog` | Authenticated | `{exports: [...], collections?: [...]}`. Only ready Exports and Collections with visible members are included. |
| `GET` | `/api/catalog/{export}/instances` | Authenticated | `{export, instances: [...]}`. Current Export's provider-side managed objects and related-resource metadata, best effort per API. Unknown Export: `404`. Unlike catalog browsing, this lookup does not require the Export to be Ready. |
| `GET` | `/api/clusters` | Authenticated | `{clusters: [...], pending?: [...]}` derived from provider Grants and heartbeat Leases. No consumer kubeconfig is stored or returned. |
| `GET` | `/api/konnector` | None | Applyable YAML: core CRDs, `kbind` namespace, konnector RBAC/ServiceAccount/Deployment, with the configured konnector image. No credentials and no side effects on GET. |
| `GET` | `/api/healthz` | None | Plain text `ok`, HTTP `200`, process check only. |

Catalog Export responses contain `name`, `title`, `apis`, optional `description`, `iconURL`, `docs`, and `relatedResources`. Related-resource rows have `resource`, `direction`, and an optional human-readable `selector` summary, not the complete Kubernetes selector structure.

Clusters contain `uid`, `lastHeartbeat`, `live`, and `bindings`, binding rows include `grant`, `export`, identity/subject, boundary namespace, heartbeat, `live`, and `apis` with `name` and `syncedCount`. A cluster is live if any binding's Lease is renewed within twice its lease duration.

Instance rows contain `api`, `namespace` when namespaced, `name`, `createdAt`, and consumer UID/identity when attributable. Related rows additionally have `related: true` and `direction`. The endpoint never returns Secret data. The [inventory guide](../../../usage/catalog.md#cluster-and-instance-inventories) explains attribution and best-effort omissions.

### Bind, pickup, and apply

| Method | Path | Authentication | Important semantics |
| --- | --- | --- | --- |
| `POST` | `/api/bind` | Authenticated | JSON body `{"export":"widgets"}`. Create/update the identity's Grant, wait for issuer Ready, then return `{grant, pickupURL, expiresAt}`. `pickupURL` is currently relative to the gateway root. |
| `GET` | `/api/bundle/{token}` | Possession of token, no session required | Single-use pickup. Default response is YAML containing Secret + Connection + ClusterBinding. `Accept: application/json` returns `{"bundle":[...objects...]}` instead. |
| `POST` | `/api/apply` | Authenticated, route enabled only by `--enable-apply` | JSON body with base64 consumer `kubeconfig`, optional `export`, and optional `installKonnector`. Apply directly from the gateway. Returns `{"applied":["Kind/name",...]}`. |

Bind behavior:

- Body must contain a nonempty `export`, no namespace, binding-kind, conflict-policy override, or collection-bind parameter exists.
- Unknown Export: `404`. Non-ready Export or provider-side failure: `502`.
- Issuer wait timeout: `504`, normally after 30 seconds. There is no command-line flag for this timeout. Retry the bind after checking the issuer and token controller, the deterministic Grant prevents duplicate records for the same identity/Export.
- Every successful bind replaces any unused pickup token for that Grant. The default pickup expiry is five minutes, not the credential lifetime.

Pickup behavior:

- Unknown, expired, or previously consumed token: `410`.
- Consumption is recorded atomically on the Grant before bundle assembly. Later assembly/response failure requires a new bind, not a retry of the same URL. A provider read/assembly failure can return `502` after consumption.
- YAML download uses a `<grant>.yaml` attachment filename. JSON contains the same object material, including credentials.
- The issued kubeconfig uses a long-lived ServiceAccount token. Pickup expiry, browser logout, and session expiry do not revoke it.
- Treat the URL and response as secrets. Exclude them from shared caches, request logs, analytics, and link scanners.

Apply behavior:

```json
{
  "export": "widgets",
  "kubeconfig": "<base64-encoded-consumer-kubeconfig>",
  "installKonnector": true
}
```

`export` may be omitted for konnector installation only. Omitting `export` with `installKonnector: false` is rejected. With an Export, the handler follows the same Grant provisioning path but builds and applies the bundle directly, without consuming a pickup URL. It ensures the `kbind` namespace even when installation is skipped.

Malformed JSON/base64/kubeconfig and “nothing to do” requests return `400`, issuance timeout returns `504`, provider or apply failures generally return `502`. Disabling the feature leaves `/api/apply` unregistered, clients should use `/api/provider.applyEnabled`, not assume a particular disabled-route status.

The gateway must be able to reach the consumer API and use the submitted kubeconfig from its own environment. Base64 is not encryption. Server-side apply forces field ownership, and operations are sequential, not transactional. An error does not imply earlier objects were rolled back.

### Browser and OIDC endpoints

| Method | Path | Authentication | Behavior |
| --- | --- | --- | --- |
| `GET` | `/` | Page itself is public | Embedded static app, its authenticated API calls send an unauthenticated browser to `/login`. |
| `GET` | `/login` | None | Login page with OIDC sign-in. |
| `GET` | `/api/auth/oidc/login` | None | Start code+PKCE login, optional `redirect` selects final landing location. |
| `GET` | `/api/auth/oidc/callback` | Valid OIDC flow/state cookie | Exchange code, verify ID token and nonce, issue session, redirect. |
| `GET` | `/api/auth/logout` | None | Clear the session cookie and redirect to `/login`, not bearer-token or Grant revocation. |

Login `redirect` accepts a local path (default `/`) or an HTTP(S) URL whose host is exactly `localhost`, `127.0.0.1`, or `::1`. A loopback redirect gets the session token appended as the `token` query parameter for the CLI. External hosts and scheme-relative redirects are rejected with `400`.

Login-state cookie `kbind_oidc_state` has a browser Max-Age of ten minutes and path `/api/auth/oidc/`. The session cookie uses the configured session TTL and path `/`. Both are HttpOnly and SameSite=Lax, Secure depends on `r.TLS`, not `--external-url` or forwarded headers. The state token is encoded using the same codec/keys as sessions, the cookie's ten-minute Max-Age should not be confused with a separate server-side flow store.

The app's Catalog and Clusters are in-page views, not additional REST endpoints or browser deep-link routes. There is no browser token-management page, Export editor, Grant revoke endpoint, or `/api/auth/refresh`.

### Error envelopes

Gateway handlers normally return `{"error":"message"}` for errors: authentication failures are `401`, malformed bind/apply requests `400`, upstream Kubernetes failures commonly `502`. OIDC handlers and standard HTTP routing/file-serving errors may be plain text instead. Do not assume every non-2xx response is JSON.

### Calling with a provider-cluster token

For an existing provider ServiceAccount `portal` in namespace `default`, when TokenReview authentication is enabled:

```sh
TOKEN="$(kubectl --context=provider -n default create token portal)"
curl -fsS https://bind.example.com/api/catalog \
  -H "Authorization: Bearer $TOKEN"
curl -fsS https://bind.example.com/api/bind \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"export":"widgets"}'
unset TOKEN
```

The response's `pickupURL` can be retrieved without that bearer token. Download once into a protected file, inspect it, and apply using a consumer kubeconfig after core installation. This is also possible with a valid kbind session bearer token, the CLI normally handles issuance and pickup itself.

## Binary flags

Flags below are defined in `cmd/backend/main.go`. Duration values use Go duration syntax such as `30s`, `5m`, or `12h`. See `./bin/backend -help` for library-added logging and Kubernetes client flags as well.

### Modules and CRDs

| Flag | Binary default | Meaning / Helm value |
| --- | --- | --- |
| `--enable-gateway` | `true` | HTTP API and UI. `modules.gateway` |
| `--enable-issuer` | `true` | Grant provisioning and Export validation controllers. `modules.issuer` |
| `--enable-reaper` | `false` | Lease-based stale Grant sweeper. `modules.reaper` |
| `--enable-apply` | `false` | Add browser-apply route to gateway. `modules.apply` |
| `--install-crds` | `true` | Install/refresh service-layer CRDs at process startup, independently of enabled modules. Helm's `installCRDs` controls templates only, pass this flag in `extraArgs` to disable process self-install. |

### Gateway and issued kubeconfigs

| Flag | Binary default | Meaning / Helm value |
| --- | --- | --- |
| `--listen-address` | `:8080` | HTTP bind address. Chart constructs `:<listenPort>`, `listenPort: 8080` |
| `--provider-name` | `kbind` | Human-facing provider name. `providerName` |
| `--external-url` | Empty | Public gateway base URL used to default OIDC callback. Empty falls back to `http://localhost` plus listen address, intended for a `:PORT` dev address. `externalURL` |
| `--pickup-ttl` | `5m` | One-time pickup URL lifetime, not credential TTL. `pickupTTL` |
| `--konnector-image` | `ghcr.io/kbind-dev/konnector:latest` | Image in `/api/konnector` manifests and browser installation. `konnectorImage` |
| `--external-address` | Empty | Provider API URL embedded in issued kubeconfigs, empty uses backend REST config host. `externalAddress` |
| `--external-ca-file` | Empty | PEM CA bundle embedded in issued kubeconfigs, empty uses token Secret `ca.crt`. No direct chart value, supply file and `extraArgs`. |
| `--issuer-scope` | `Cluster` | Granted-resource RBAC in all namespaces (`Cluster`) or boundary-only (`Namespace`). `issuerScope` |

`externalURL` and `externalAddress` are different endpoints. The first is reached by browser/CLI clients, the second by the consumer konnector. Namespace scope does not remap namespaces or change the bundle's `ClusterBinding` kind.

### Sessions

| Flag | Binary default | Meaning / Helm value |
| --- | --- | --- |
| `--cookie-signing-key` | Empty | Standard base64 text of HMAC key bytes, use 32/64 bytes. Empty generates a random per-process 64-byte key. `cookieKeys.existingSecret`, key `signingKey` |
| `--cookie-encryption-key` | Empty | Standard base64 text of AES key bytes (16/24/32). Empty generates a random per-process 32-byte key. Same Secret, key `encryptionKey` |
| `--session-ttl` | `12h` | Session token and session cookie lifetime. `sessionTTL` |

Both keys must match across replicas. Missing either key breaks that part of cross-replica/restart continuity. The binary has no direct environment-variable configuration for these flags, the chart injects Secret values into environment variables and expands them in container arguments.

### OIDC and Kubernetes authentication

| Flag | Binary default | Meaning / Helm value |
| --- | --- | --- |
| `--oidc-issuer-url` | Empty | OIDC discovery issuer. Required for gateway except mock mode. `oidc.issuerURL` |
| `--oidc-client-id` | Empty | OIDC client ID. Required for gateway except mock mode. `oidc.clientID` |
| `--oidc-client-secret` | Empty | Confidential client's secret, as required by issuer registration. `oidc.existingSecret`, key `clientSecret` |
| `--oidc-ca-file` | Empty | PEM CA bundle for OIDC HTTPS, empty uses system roots. A supplied bundle replaces the root pool. No direct chart mount/value, supply file and `extraArgs`. |
| `--oidc-username-claim` | `sub` | Required nonempty string claim used in stable subject. `oidc.usernameClaim` |
| `--oidc-groups-claim` | Empty | Optional string or string-array groups claim. Empty means no groups. `oidc.groupsClaim` |
| `--oidc-scopes` | `openid,profile,email` | Comma-separated requested scopes. `oidc.scopes` |
| `--oidc-redirect-url` | Empty | Defaults to `<external-url>/api/auth/oidc/callback`. `oidc.redirectURL` |
| `--oidc-mock` | `false` | Embedded auto-approving development issuer, replaces normal OIDC config. `oidc.mock` |
| `--oidc-mock-listen` | Empty | Fixed mock issuer address or random localhost port when empty. Chart uses `127.0.0.1:<oidc.mockPort>`, default chart port `5556`. |
| `--kubernetes-auth` | `false` | Additionally accept provider tokens via TokenReview. `kubernetesAuth` |

Mock mode is not safe for production. There is no public flag to skip TLS verification for a real OIDC issuer, configure trust correctly.

### Reaper

| Flag | Binary default | Meaning / Helm value |
| --- | --- | --- |
| `--reaper-ttl` | `30m` | Silence threshold for matching heartbeat Leases, or Ready age with no renewed Lease. `reaper.ttl` |
| `--reaper-revoke` | `false` | Delete stale Grants, issuer finalizer revokes credentials. `reaper.revoke` |
| `--reaper-delete-boundary` | `false` | When revoking, also delete boundary namespace if no other non-deleting Grant shares it. `reaper.deleteBoundary` |
| `--reaper-interval` | `1m` | Sweep cadence. `reaper.interval` |

When `--reaper-revoke=false`, stale marking is non-destructive. Boundary deletion only operates inside revocation, setting it alone does not enable deletion. This policy is separate from the inventory's two-lease-duration Live/Stale display.

### Controller manager

| Flag | Binary default | Meaning / Helm value |
| --- | --- | --- |
| `--metrics-bind-address` | `:8086` | Controller-manager metrics, chart constructs `:<metrics.port>`. |
| `--health-probe-bind-address` | `:8082` | Controller-manager health/readiness listener, chart constructs `:<healthProbe.port>`. |
| `--leader-elect` | `false` | Leader election for issuer/reaper controllers. Chart enables for `leaderElect: true` or `replicaCount > 1`. |
| `--leader-election-id` | `backend.kbind.io` | Leader-election Lease name. `leaderElectionID` |

The manager exists only when issuer or reaper is enabled. These addresses are not gateway endpoints, gateway-only processes do not start this manager. The binary also registers controller-runtime's `--zap-*` logging flags.

## Helm-specific settings and caveats

The chart is **`deploy/charts/backend-v2`**:

- Image defaults: repository `ghcr.io/kbind-dev/backend`, empty `image.tag` falling back to source `appVersion: "0.0.0"`. Override with an image actually built/published from the intended v2 revision.
- Gateway Service: `service.type: ClusterIP`, `service.port: 80`, `listenPort: 8080`. No Ingress/TLS or NetworkPolicy templates are included.
- `replicaCount: 1`, `leaderElect: false`, and no existing cookie key Secret by default. Increasing replicas automatically enables election, **not** key sharing.
- `rbac.create: true` provisions powerful provider administration permissions. `rbac.syncedResourceGroups: ["*"]` enables broad read inventory, narrow it deliberately. Module flags do not prune RBAC rules.
- `serviceAccount.create`, `serviceAccount.name`, and `serviceAccount.annotations` configure the runtime identity.
- `extraArgs` appends binary flags. CA files still need a Deployment mount customization, there are no generic extra-volume values.
- `installCRDs: false` alone does not disable the process's CRD self-install.
- The OIDC issuer/client ID template requirements apply even if the gateway module is disabled.

Read [backend setup](../index.md) before exposing a deployment. In particular, HTTPS termination does not make the HTTP backend set Secure cookies, browser apply forwards consumer credentials, and the default issued scope is not namespace-isolated tenancy.
