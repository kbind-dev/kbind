# API Binding

v2 binding terminates in ordinary consumer-side Kubernetes objects rather than request/response CRDs.

## Request

An authenticated caller posts an Export name to `POST /api/bind`:

```json
{"export":"widgets"}
```

The gateway requires a ready Export, creates or updates the caller's Grant, and waits for the issuer to provision credentials. The response contains `grant`, `pickupURL`, and `expiresAt`.

## Pickup and apply

`GET /api/bundle/{token}` consumes the pickup ticket once and returns a kubeconfig Secret, Connection, and ClusterBinding. The ticket defaults to a five-minute lifetime, the issued credentials remain usable until revoked.

The bundle does not include the konnector or core CRDs. Install those first, then apply the bundle using the CLI, kubectl, or an existing GitOps workflow.

## Revocation

Deleting the provider Grant asks the issuer to remove its credentials. This does not remove the consumer's core objects or automatically delete synced provider instances. For normal teardown, unbind while credentials still work.

See [catalog and issuance](../../../usage/catalog.md) for the lifecycle and the [HTTP reference](index.md) for status codes, authentication, and pickup semantics.
