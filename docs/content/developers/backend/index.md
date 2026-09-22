# Backend

The backend adds a catalog, browser login, credential issuance, and provider-side inventory to kbind. It runs **on or against the provider cluster**. The consumer runs the konnector, not the backend.

You do not need a backend to use the core `Connection`, `ClusterBinding`, and `Binding` APIs with credentials supplied by another system. See [installation](../../setup/helm.md) and the [GitOps workflow](../../usage/gitops.md) for that path.

## Modules and placement

One `backend` binary contains independently enabled modules:

| Module | Binary flag / Helm value | Default | Responsibility |
| --- | --- | --- | --- |
| Gateway | `--enable-gateway` / `modules.gateway` | `true` | HTTP API, embedded browser UI, login, catalog, bundle pickup, inventories |
| Issuer | `--enable-issuer` / `modules.issuer` | `true` | Reconcile Grants into Kubernetes credentials, validate Exports |
| Reaper | `--enable-reaper` / `modules.reaper` | `false` | Mark stale Grants using heartbeat Leases, optionally revoke and delete boundaries |
| Browser apply | `--enable-apply` / `modules.apply` | `false` | Add `POST /api/apply` to the gateway |

The gateway fronts exactly one provider: the Kubernetes configuration used by the backend process. The binary resolves that configuration through controller-runtime/client-go, using `KUBECONFIG` or an in-cluster ServiceAccount. Changing a user's gateway session does not select a different provider.

Gateway-only deployments need an issuer running elsewhere for new binds to finish. Reaper revocation also needs the issuer to process Grant finalizers. Issuer-only deployments need no OIDC configuration **in the binary**, the current Helm template nevertheless requires `oidc.issuerURL` and `oidc.clientID` unless `oidc.mock` is true, even with `modules.gateway: false`.

The bundled issuer is for plain Kubernetes: it provisions namespaces, ServiceAccounts, RBAC, and Secret-based ServiceAccount tokens. There is no selectable built-in kcp issuer.

## Development: run against a provider

From this v2 source checkout, with a provider kubeconfig and the Go version required by `go.mod`:

```sh
make backend
KUBECONFIG=./provider.kubeconfig ./bin/backend \
  --oidc-mock \
  --external-url=http://localhost:8080 \
  --external-address=https://PROVIDER-API-REACHABLE-FROM-CONSUMER
```

Open `http://localhost:8080` and sign in. The default startup installs or refreshes the `catalog.kbind.io` and `iam.kbind.io` CRDs, so this kubeconfig must permit CRD writes as well as the backend's other operations.

`--oidc-mock` starts a local issuer and **auto-approves every login as the mock user**. It is only for an isolated development environment. It does not simulate production access policy or user separation. The default mock listen port is random, set `--oidc-mock-listen=127.0.0.1:5556` when a stable, port-forwardable issuer URL is needed.

For an in-cluster development deployment, explicitly select a backend image built from this checkout:

```sh
helm upgrade --install backend ./deploy/charts/backend-v2 \
  --kube-context=provider \
  --namespace=kbind-backend --create-namespace \
  --set fullnameOverride=kbind-backend \
  --set image.repository=ghcr.io/kbind-dev/backend \
  --set image.tag=dev \
  --set oidc.mock=true \
  --set externalURL=http://localhost:8080 \
  --set externalAddress=https://PROVIDER-API-REACHABLE-FROM-CONSUMER
```

The `dev` image must already be available to that cluster, this command does not build, publish, or load it. For example, `make image-backend` builds `ghcr.io/kbind-dev/backend:dev` locally. Load it into your development cluster or push your own tagged image first.

Forward both the gateway and the chart's fixed mock issuer port:

```sh
kubectl --context=provider -n kbind-backend \
  port-forward deployment/kbind-backend 8080:8080 5556:5556
```

The host browser and backend must both be able to reach the issuer URL. A mock login cannot work through a gateway-only port-forward when the issuer port is unreachable.

## Production prerequisites

Before deploying:

1. Install the provider's service CRDs and service controllers. The backend catalogs APIs, it does not implement the services behind them.
2. Choose a backend image and a konnector image from the intended v2 revision. The source chart has placeholder `version` and `appVersion` **`0.0.0`**, an empty `image.tag` therefore selects `ghcr.io/kbind-dev/backend:0.0.0`. Override it. The installer/CLI default konnector image is **`ghcr.io/kbind-dev/konnector:latest`**, not the chart's version.
3. Provide an HTTPS gateway URL, a trusted OIDC issuer, and an OIDC client with the callback `https://YOUR-GATEWAY/api/auth/oidc/callback`.
4. Provide a provider API address reachable from the **consumer konnector**. An in-cluster address such as `https://kubernetes.default.svc` is usually not suitable for a consumer in another cluster.
5. Review the backend and issued-credential RBAC described below. Authenticated access to this gateway is a significant trust decision.

Use the local chart at `deploy/charts/backend-v2`, not a v0.x chart path or an assumed released v2 chart version. For images you build yourself:

```sh
make image-backend BACKEND_IMAGE=registry.example.com/platform/backend:v2-example
make image IMAGE=registry.example.com/platform/konnector:v2-example
docker push registry.example.com/platform/backend:v2-example
docker push registry.example.com/platform/konnector:v2-example
```

Replace the registry and illustrative tags with your own published, revision-pinned images. Configure registry credentials using `imagePullSecrets` when required.

### OIDC and shared session keys

Create the following Secrets in the release namespace. Populate the OIDC client secret using your normal secret-management process, its key must be `clientSecret`. Do not put real secrets in a committed values file.

```sh
kubectl --context=provider create namespace kbind-backend
kubectl --context=provider -n kbind-backend create secret generic kbind-oidc \
  --from-literal=clientSecret="$OIDC_CLIENT_SECRET"
kubectl --context=provider -n kbind-backend create secret generic kbind-cookie-keys \
  --from-literal=signingKey="$(openssl rand -base64 64)" \
  --from-literal=encryptionKey="$(openssl rand -base64 32)"
```

`signingKey` and `encryptionKey` contain **base64 text** of the random key bytes. Kubernetes Secret storage encodes that text again in `.data`, do not substitute raw bytes for the text expected by the backend flags. Recommended decoded sizes are 32 or 64 bytes for the HMAC signing key, and 16, 24, or 32 bytes for the AES encryption key.

Example `backend-values.yaml`:

```yaml
fullnameOverride: kbind-backend
replicaCount: 2
image:
  repository: registry.example.com/platform/backend
  tag: v2-example
konnectorImage: registry.example.com/platform/konnector:v2-example
providerName: Example platform
externalURL: https://bind.example.com
externalAddress: https://provider-api.example.com:6443
issuerScope: Cluster
modules:
  gateway: true
  issuer: true
  reaper: false
  apply: false
oidc:
  issuerURL: https://sso.example.com
  clientID: kbind
  existingSecret: kbind-oidc
  usernameClaim: sub
  groupsClaim: groups
  scopes: openid,profile,email,groups
cookieKeys:
  existingSecret: kbind-cookie-keys
sessionTTL: 12h
pickupTTL: 5m
rbac:
  syncedResourceGroups:
    - example.org
    - ""
```

Request the `groups` scope only if your identity provider supports it. The empty API group above enables inventory reads of ConfigMaps as well as the example custom API, adjust the groups to what your catalog uses.

```sh
helm upgrade --install backend ./deploy/charts/backend-v2 \
  --kube-context=provider \
  --namespace=kbind-backend \
  --values=backend-values.yaml
kubectl --context=provider -n kbind-backend rollout status deployment/kbind-backend
```

The chart exposes a ClusterIP Service on port 80, targeting HTTP port 8080. It does **not** create an Ingress, Gateway API route, TLS certificate, or NetworkPolicy. Supply those through your platform.

### HTTPS and cookies

The binary serves HTTP with `ListenAndServe`, it has no TLS-certificate flags. Terminate HTTPS at a trusted proxy and restrict direct access to the backend. Use HTTPS for production gateway, OIDC, and provider API endpoints.

Important current behavior:

- Both `kbind_session` and `kbind_oidc_state` are `HttpOnly`, `SameSite=Lax` cookies. The session cookie uses `/`, the login-state cookie uses `/api/auth/oidc/`.
- The cookie `Secure` attribute is set only when the backend request has `r.TLS != nil`. With this binary's HTTP listener behind TLS termination it is **not automatically set**, even when `externalURL` is HTTPS. `X-Forwarded-Proto` does not change that behavior. Configure the HTTPS proxy to add `Secure` to both cookies, and verify the browser's actual `Set-Cookie` responses.
- The encrypted, signed session token is accepted either as the cookie or as `Authorization: Bearer <session-token>`. This is a kbind session token, not an arbitrary OIDC ID token or access token.
- Sessions default to 12 hours. There is no refresh endpoint or per-session server-side revocation store. Browser logout clears the cookie, an already copied bearer token remains usable until expiry or key replacement.
- Missing keys produce random per-process keys. Supply **both** shared keys for restart-stable sessions and multiple gateway replicas. Replacing the keys invalidates existing sessions and in-progress login state.

OIDC uses authorization code flow with PKCE, state, and nonce validation. `oidc.usernameClaim` defaults to `sub`, the resulting identity subject is `<issuer-url>#<claim-value>`. Changing the issuer or username claim can create new identities, Grants, and boundary namespaces. Groups are recorded as identity metadata, they are not an Export authorization policy.

For a private OIDC CA, the binary supports `--oidc-ca-file`. For a provider CA that differs from the ServiceAccount token Secret's `ca.crt`, use `--external-ca-file`. These are distinct trust paths. The chart has no direct CA-file or extra-volume values: make the files available with an appropriate Deployment customization and pass the flags through `extraArgs`. Merely passing a local filename in Helm values does not mount that file into the container.

### Kubernetes TokenReview authentication

Set `kubernetesAuth: true` (binary: `--kubernetes-auth`) to additionally accept provider-cluster bearer tokens. The backend submits a TokenReview to its provider, the chart includes `create` on `authentication.k8s.io/tokenreviews`. The identity subject is `kubernetes#<username>`.

This is for API integrations that already hold a provider identity. It is not a second interactive login method in the browser or CLI, and does not replace OIDC configuration in the gateway binary. `kubectl bind login` still uses OIDC. See the [HTTP reference](http/index.md) for request examples.

The gateway authenticates callers but does not perform per-Export entitlement checks or caller-scoped inventory filtering. Every authenticated caller can browse the ready catalog, bind its Exports, and read the provider's cluster and instance inventories. TokenReview verifies identity, not authorization to those operations. Do not expose this as a tenant-isolated marketplace without an additional access-control design.

## Credential scope and installer privileges

`issuerScope: Cluster` is the default. Each Grant gets permissions on its named APIs across provider namespaces, including their `/status` resources, plus namespace creation. Related-resource rules add Secret/ConfigMap access: read-only for `FromProvider`, read/write for `FromConsumer`.

`issuerScope: Namespace` puts those resource permissions only in the identity's boundary namespace. It does not rename namespaces, change the generated `ClusterBinding` into a `Binding`, or authorize cluster-scoped resources. Identity-preserving sync requires compatible consumer namespace names and provider list/watch permissions, do not treat this setting as a transparent drop-in isolation switch for the default cluster-wide bundle.

Both scopes include cluster-wide CRD reads, a read of `kube-system` for provider identity, and heartbeat Lease permissions inside the boundary. Related-resource selectors constrain konnector sync, **not** Kubernetes RBAC: the issued role does not restrict reads to the selected labels or names.

The backend's own chart RBAC includes namespace/credential management, `escalate` and `bind` on Roles/ClusterRoles, and wildcard inventory reads by default. Narrow `rbac.syncedResourceGroups`, and use reviewed custom RBAC with `rbac.create: false` if necessary. Disabling a module does not automatically remove its chart RBAC.

Keep browser apply off unless intentionally granting the gateway access to consumer API endpoints and credentials. The browser sends an entire base64-encoded kubeconfig, base64 is not encryption. Use a trusted, self-contained kubeconfig, avoid local-file and exec-plugin dependencies, restrict gateway egress, and exclude credentials/request bodies from logs. CLI apply keeps the consumer kubeconfig local, it does not use `/api/apply`.

All built-in install/apply paths use server-side apply with **forced field ownership**. They can overwrite drift in existing kbind installation resources. Review the embedded konnector's broad cluster RBAC, pin its image, and do not mix automatic install/upgrade with a separately managed Helm installation without understanding the ownership consequences.

## CRDs, replicas, and operations

- `installCRDs` in Helm controls chart-rendered service-layer CRDs only. `--install-crds` independently defaults to `true` in the process. To manage CRDs out of band, install them first, set `installCRDs: false`, and add `--install-crds=false` to `extraArgs`.
- With `replicaCount > 1`, the chart enables leader election automatically. Gateway replicas serve requests independently, issuer/reaper controllers run under leader election. Set shared session keys yourself: the chart does not enforce their presence.
- Pickup state lives in Grant annotations, not gateway memory. It works across replicas. Back up provider Kubernetes state according to your credential and recovery policy, sessions also depend on the key Secret.
- `/api/healthz` is a process HTTP check, not proof of functioning OIDC, catalog validation, or credential issuance. Check an actual login, a ready Export, and a bind before declaring the deployment usable.

Enable the reaper conservatively with `modules.reaper: true` and `reaper.revoke: false`. Review its `Stale` conditions before enabling revocation. The [catalog guide](../../usage/catalog.md) describes lifecycle and destructive cleanup. Consult the [flag and endpoint reference](http/index.md) and [troubleshooting](../../usage/troubleshooting.md) for operational detail.
