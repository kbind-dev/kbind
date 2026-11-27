---
title: OIDC Authentication
weight: 30
---

# OIDC Authentication

The kbind backend supports OpenID Connect (OIDC) authentication for securing API access. There are two modes of operation: external OIDC providers and an embedded OIDC provider for development.

## External OIDC Provider (Production)

For production deployments, use an external OIDC provider such as:

- Dex
- Keycloak
- Auth0
- Google
- Microsoft Azure AD
- Any OIDC-compliant provider

### Configuration

Configure the backend using the v2 flags:

```bash
./bin/backend \
  --external-url=https://bind.example.com \
  --oidc-issuer-url=https://your-oidc-provider.com \
  --oidc-client-id=your-client-id \
  --oidc-client-secret="$OIDC_CLIENT_SECRET" \
  --oidc-redirect-url=https://bind.example.com/api/auth/oidc/callback
```

Register the exact callback with your issuer. Use `--oidc-ca-file` for a private CA, `--oidc-username-claim` for the stable identity claim (default `sub`), and `--oidc-groups-claim` if groups should be recorded.

### External OIDC Flow

1. User initiates authentication by accessing the gateway login page.
2. Backend redirects to the OIDC authorization endpoint with state, nonce, and PKCE.
3. User authenticates with the OIDC provider.
4. Provider redirects to the backend callback with an authorization code.
5. Backend exchanges the code, verifies the ID token, and issues a signed, encrypted session.
6. User accesses protected APIs using the session cookie or token.

## Embedded OIDC Provider (Development Only)

For development and testing, kbind includes an embedded provider that avoids setting up an external authentication service:

```bash
./bin/backend --oidc-mock --external-url=http://localhost:8080
```

### Embedded OIDC Flow

1. Backend initializes the mock OIDC issuer and its client credentials.
2. User starts login through the gateway.
3. The mock issuer auto-approves the fixed development identity.
4. Backend completes the same callback and session flow.

The mock issuer uses a separate port. With port forwarding, expose that port as well as the gateway, see [Backend setup](../index.md#development-run-against-a-provider).

## Security Considerations

### External OIDC (Production)

- Use HTTPS for all endpoints and validate provider certificates.
- Use strong client secrets and exact redirect URI restrictions.
- Share both signing and encryption keys across gateway replicas.
- Configure the TLS proxy to add `Secure` to session and login-state cookies: the HTTP backend does not infer it from `externalURL` or forwarded headers.
- Monitor for security updates to the OIDC provider.

### Embedded OIDC (Development Only)

- **Never use in production**: every login is auto-approved.
- It is intended only for isolated local development and testing.
- It does not model distinct users or production authorization.

## Access Control

v2 does not have `--oidc-allowed-groups` or `--oidc-allowed-users`. OIDC groups are identity metadata, not per-Export authorization. All authenticated gateway callers can bind ready Exports and read the provider's inventories. Control access to the gateway accordingly, do not assume the old group-based binding policy is still enforced.

Provider-cluster bearer tokens can additionally be authenticated using `--kubernetes-auth` and TokenReview. This does not replace gateway OIDC login configuration. See [authentication conventions](../http/index.md#authentication-conventions).

## Troubleshooting

### Common Issues

1. **Certificate validation failures**: configure `--oidc-ca-file` with the issuer's trusted CA.
2. **Callback URL mismatches**: register `/api/auth/oidc/callback` at the public gateway URL.
3. **Token validation errors**: confirm the issuer URL and configured username claim.
4. **Network connectivity**: verify both browser and backend can reach the issuer.
5. **Sessions lost across replicas/restarts**: configure both shared session keys.

See the [backend reference](../http/index.md) for complete flags and cookie lifetimes.
