---
title: "kbind v2: Getting Started"
description: "Connect two Kubernetes clusters with kbind v2 and synchronize a simulated Database API without a backend."
icon: material/database-sync
date: 2026-11-24
authors:
  - olamilekan000
slug: kbind-v2-getting-started
---

# kbind v2: Getting Started

*Part 1: Connect two Kubernetes clusters and explore the new API model.*

Kubernetes gives us a familiar way to request services: declare what we want in a resource and let a controller work toward that desired state. Consuming a service often means installing its operator in every cluster that needs it. **kbind**, formerly kube-bind, offers another approach: providers operate services centrally while consumers create and inspect resources in their own clusters using familiar tools such as `kubectl`.

A consumer-side agent called the **konnector** synchronizes resource specifications to the provider and brings status back. In v2, this synchronization core is separated from the optional backend for authentication, catalog discovery, and credential issuance. We’ll start with the core, using a startup’s checkout database as our example.

<!-- more -->

## What changed in v2?

The basic relationship stays familiar: the consumer declares desired state, and the provider operates the service. What changes is the machinery connecting them.

| Earlier kube-bind model | v2 |
|---|---|
| Provider connection spread across binding objects and credentials | An explicit `Connection` references provider credentials |
| Earlier binding/export machinery | `Binding` and `ClusterBinding` select APIs to synchronize |
| Backend-driven binding protocol | Core objects can be supplied directly; backend onboarding is optional |
| `BoundSchema` intermediates | CRDs are pulled directly or synthesized from OpenAPI |
| Namespace mapping and isolation modes | The stock engine preserves scope, namespace, and name |
| Permission claims and reference machinery | Explicit selectors for related Secrets and ConfigMaps |

The core uses three resource kinds under `core.kbind.io/v1alpha1`:

| Kind | Purpose |
|---|---|
| `Connection` | Connect to a provider and control API discovery and schema delivery |
| `ClusterBinding` | Synchronize selected APIs across the consumer cluster |
| `Binding` | Synchronize selected namespaced APIs within one consumer namespace |

A Kubernetes Secret supplies the provider kubeconfig. The optional service layer adds catalog `Export` and `Collection` resources, plus an IAM `Grant` for credential issuance. We’ll cover those in Part 2.

Earlier objects such as `APIServiceBinding`, `BoundSchema`, and `APIServiceNamespace` no longer drive the core. The new consumer-side `ClusterBinding` also has a different meaning from the old provider-side kind. These are new APIs, not manifests you can upgrade by changing `apiVersion`. Existing deployments need a deliberate migration; there is no supported automatic conversion of old bindings. [Migration guide](../../usage/migration.md)

## Example:

Imagine a startup whose application team needs a database for its checkout service. The team declares this in its own cluster:

```yaml
apiVersion: startup.example/v1
kind: Database
metadata:
  name: checkout-db
  namespace: default
spec:
  plan: starter
  storageGB: 10
```

The provider receives the request, and the consumer receives the resulting status. We’ll also synchronize a credentials Secret and a settings ConfigMap from the provider.

**This is a simulated database API.** We’ll create real Kubernetes resources and verify synchronization, but we won’t provision PostgreSQL or another database server. The credentials are static demo values, and we’ll manually simulate the provider controller’s status update.

## 1. Prepare the source and samples

You need Docker running, kind, kubectl, Git, make, and Go matching the repository’s `go.mod`. This walkthrough uses the v2 development source and disposable clusters.

Clone the repository and select the revision used for this example:

```bash
git clone --branch v2-next https://github.com/kbind-dev/kbind.git
cd kbind
git checkout 85d25389c44c57cee5b324eb1559533535f172cc

make konnector
```

Save these three manifests in the root of your `kbind` checkout:

| File | Applied to | Contents |
|---|---|---|
| [provider-database.yaml](../assets/kbind-v2-getting-started/provider-database.yaml) | Provider | Database CRD, credentials Secret, settings ConfigMap |
| [database-binding.yaml](../assets/kbind-v2-getting-started/database-binding.yaml) | Consumer | Connection and ClusterBinding |
| [checkout-db.yaml](../assets/kbind-v2-getting-started/checkout-db.yaml) | Consumer | The database request |

Run the remaining commands from that checkout root.

## 2. Create the provider and consumer clusters

Use fresh clusters with these names:

```bash
kind create cluster --name startup-provider \
  --kubeconfig "$PWD/provider.kubeconfig" --wait 120s

kind create cluster --name startup-consumer \
  --kubeconfig "$PWD/consumer.kubeconfig" --wait 120s
```

Both kubeconfigs use host-mapped endpoints. We’ll therefore run the konnector on our workstation. Those addresses would not work unchanged inside a Pod in another cluster.

## 3. Publish the Database API

Apply the provider sample and wait for its CRD:

```bash
kubectl --kubeconfig=provider.kubeconfig \
  apply -f provider-database.yaml

kubectl --kubeconfig=provider.kubeconfig wait \
  --for=condition=Established \
  crd/databases.startup.example --timeout=120s
```

The CRD defines `Database` with `spec.plan`, `spec.storageGB`, and `status.phase`. It also carries the export label:

```yaml
metadata:
  name: databases.startup.example
  labels:
    core.kbind.io/exported: "true"
```

For CRD-based discovery, that label opts the API into kbind’s export discovery. Provider RBAC separately controls what the supplied credentials can access. The same manifest creates `checkout-db-credentials` and `checkout-db-settings` on the provider, ready for related-resource synchronization.

## 4. Connect the consumer

Install the three core API definitions:

```bash
kubectl --kubeconfig=consumer.kubeconfig apply \
  -f sdk/config/crd/core.kbind.io_connections.yaml \
  -f sdk/config/crd/core.kbind.io_clusterbindings.yaml \
  -f sdk/config/crd/core.kbind.io_bindings.yaml

kubectl --kubeconfig=consumer.kubeconfig wait \
  --for=condition=Established \
  crd/connections.core.kbind.io \
  crd/clusterbindings.core.kbind.io \
  crd/bindings.core.kbind.io \
  --timeout=120s
```

Create the namespace and credential Secret:

```bash
kubectl --kubeconfig=consumer.kubeconfig create namespace kbind

kubectl --kubeconfig=consumer.kubeconfig -n kbind \
  create secret generic database-provider-kubeconfig \
  --from-file=kubeconfig=provider.kubeconfig
```

This copies administrator credentials for the disposable provider into the consumer. Use restricted credentials and secure Secret delivery for a real deployment. Keep these generated kubeconfigs out of version control.

Now apply the binding:

```bash
kubectl --kubeconfig=consumer.kubeconfig \
  apply -f database-binding.yaml
```

Open that file to see the relationship. `Connection/database-provider` references the credential Secret and selects CRD-based schema discovery. `ClusterBinding/startup-databases` references that Connection, selects `databases.startup.example`, and defines selectors for the related resources.

**The Connection establishes the provider link; the binding activates synchronization for the selected API.**

## 5. Start the konnector

In a second terminal, from the same checkout:

```bash
KUBECONFIG="$PWD/consumer.kubeconfig" ./bin/konnector
```

Leave it running. Back in the first terminal, wait for the connection, binding, and imported Database CRD:

```bash
kubectl --kubeconfig=consumer.kubeconfig wait \
  --for=condition=Ready \
  connection/database-provider \
  clusterbinding/startup-databases \
  --timeout=120s

kubectl --kubeconfig=consumer.kubeconfig wait \
  --for=condition=Established \
  crd/databases.startup.example --timeout=120s
```

The consumer now serves the Database API without running a database-specific controller.

## 6. Request the checkout database

Create the request on the consumer:

```bash
kubectl --kubeconfig=consumer.kubeconfig \
  apply -f checkout-db.yaml
```

Wait for the provider copy, then verify both requested fields:

```bash
kubectl --kubeconfig=provider.kubeconfig -n default wait \
  --for=create database/checkout-db --timeout=120s

kubectl --kubeconfig=provider.kubeconfig -n default wait \
  --for=jsonpath='{.spec.plan}'=starter \
  database/checkout-db --timeout=120s

kubectl --kubeconfig=provider.kubeconfig -n default wait \
  --for=jsonpath='{.spec.storageGB}'=10 \
  database/checkout-db --timeout=120s
```

These checks establish that the desired state reached the provider. A real database controller would act on that provider-side resource.

## 7. Return status to the application team

Simulate the provider controller reporting that the database is ready:

```bash
kubectl --kubeconfig=provider.kubeconfig -n default \
  patch database checkout-db --subresource=status --type=merge \
  -p '{"status":{"phase":"Ready"}}'
```

Wait for that status on the consumer:

```bash
kubectl --kubeconfig=consumer.kubeconfig -n default wait \
  --for=jsonpath='{.status.phase}'=Ready \
  database/checkout-db --timeout=120s

kubectl --kubeconfig=consumer.kubeconfig -n default get databases
```

Expected output:

```text
NAME          PLAN      STORAGEGB   PHASE
checkout-db   starter   10          Ready
```

The application team can inspect its request and the provider’s reported state from its own cluster.

## 8. Verify credentials and settings synchronization

The binding selects the provider’s labeled Secret and ConfigMap independently of the Database instance. Wait for their consumer copies:

```bash
kubectl --kubeconfig=consumer.kubeconfig -n default wait \
  --for=create \
  secret/checkout-db-credentials \
  configmap/checkout-db-settings \
  --timeout=120s
```

Check ownership without printing Secret data:

```bash
kubectl --kubeconfig=consumer.kubeconfig -n default get \
  secret/checkout-db-credentials configmap/checkout-db-settings \
  -o 'custom-columns=NAME:.metadata.name,BINDING:.metadata.annotations.core\.kbind\.io/related-binding'

kubectl --kubeconfig=consumer.kubeconfig \
  get clusterbinding startup-databases \
  -o jsonpath='{.metadata.uid}{"\n"}'
```

Both `BINDING` values should match the ClusterBinding UID. The related resources were created on the provider; the consumer manifests contain only the binding inputs and the Database request.

## 9. Clean up

Stop the konnector with Ctrl-C, then remove the disposable clusters and credentials:

```bash
kind delete cluster --name startup-consumer
kind delete cluster --name startup-provider

rm consumer.kubeconfig provider.kubeconfig
```

For existing deployments, follow the documented unbinding procedure while controllers and credentials remain available. Binding deletion can trigger cleanup of synchronized resources. [Unbinding guidance](../../usage/gitops.md#pruning-is-unbinding)

## Coming in Part 2

We supplied credentials and bindings directly. A provider serving multiple consumers also needs an onboarding workflow: authenticate the consumer, show available services, and issue credentials.

In [kbind v2: From Core Sync to Service Onboarding](2026-11-26-kbind-v2-service-onboarding.md), we’ll add the optional backend and explore `Export`, `Collection`, and `Grant`. The backend will produce the same core inputs we created manually here.
