# Integrations

Explore the integrations below to see how you can use kbind with provider-side operators. These retain the original operator setup and example resources, with binding instructions adapted for v2.

## Prerequisites

Install the [backend](../../developers/backend/index.md), a matching [konnector](../../setup/helm.md), and the [v2 CLI](../../setup/kubectl-plugin.md). Examples use `provider` and `consumer` kubeconfig contexts and a separate `consumer.kubeconfig` for CLI apply, substitute your own names and gateway URL.

Run provider setup against `provider` and consumer manifests against `consumer`. The guides explicitly switch context before each phase. Wait for the exported consumer CRD to be Established before creating service instances.

v2 preserves namespace, name, and scope. These examples are for disposable, trusted development clusters, not isolated multi-tenant production deployments. Named and label-based related-resource selectors replace the old JSONPath claims, they do not restrict the RBAC granted by provider credentials.

## Available Integrations

- **[cert-manager](cert-manager.md)**: Certificate management as a service.
- **[CloudNativePG](cloudnativepg.md)**: Database-as-a-Service on Kubernetes.
- **[Crossplane](crossplane.md)**: Example Database-as-a-Service using Crossplane provider.
- **[kro](kro.md)**: Integrate with kro to serve LoadBalancer-as-a-Service example.
