---
title: Provisioning PostgreSQL Across Kubernetes Clusters with kbind
description: Learn how to use kbind v2 and CloudNativePG to provision PostgreSQL on a provider cluster, synchronize credentials, and securely access the database from a consumer cluster.
weight: 30
---

# Provisioning PostgreSQL Across Kubernetes Clusters with kbind

One of the use cases for kbind is enabling platform teams to
offer Kubernetes-native services to consumers without requiring
them to install and manage the underlying operators in their
own clusters.

In this example, we'll use [CloudNativePG](https://cloudnative-pg.io/)
and kbind v2 to provision PostgreSQL across two Kubernetes clusters.
The provider cluster runs the CloudNativePG operator and manages
the database lifecycle, while the consumer requests a PostgreSQL
instance by creating a `Cluster` resource in its own cluster.

kbind synchronizes the resource to the provider and returns its
status and generated credentials to the consumer. We'll then
connect to the PostgreSQL instance from the consumer cluster
and execute SQL queries over a verified TLS connection.

This example uses a core binding, so the optional backend and CLI
login are not required. For service catalog onboarding, see
[Part 2](../../blog/posts/2026-11-26-kbind-v2-service-onboarding.md).

## Prerequisites

- Two trusted Kubernetes clusters and kubectl access to both.
- Helm 3 and a default StorageClass on the provider.
- A provider API endpoint reachable from Pods in the consumer cluster.
- Provider credentials in a self-contained kubeconfig, with permission to discover
  the exported CRD, manage PostgreSQL `Cluster` resources, and read related Secrets.

The konnector runs as a Deployment in the consumer cluster. Helm installs its
three core CRDs (`Connection`, `ClusterBinding`, and `Binding`), RBAC, and Deployment.
This walkthrough uses the published v2.0.0-rc7 release candidate. No source checkout
or local compilation is required. Resource names and namespaces are preserved.

Set the paths to your administrator kubeconfigs and the provider access kubeconfig
that the konnector will use:

```bash
export PROVIDER_KUBECONFIG=/path/to/provider.kubeconfig
export CONSUMER_KUBECONFIG=/path/to/consumer.kubeconfig
export PROVIDER_ACCESS_KUBECONFIG=/path/to/provider-access.kubeconfig
```

The access kubeconfig must embed its CA and credentials. Local file paths and
workstation-specific credential plugins will not work in the konnector Pod.
Its API server address must be reachable from that Pod, rather than a workstation
loopback address.

### Optional: create local clusters with kind

If you do not have two clusters, use kind with Docker. Both nodes share the
Docker `kind` network. The internal provider kubeconfig uses a node DNS name
reachable from the consumer cluster:

```bash
export PROVIDER_KUBECONFIG=/tmp/cnpg-provider.kubeconfig
export CONSUMER_KUBECONFIG=/tmp/cnpg-consumer.kubeconfig
export PROVIDER_ACCESS_KUBECONFIG=/tmp/cnpg-provider-internal.kubeconfig
kind create cluster --name cnpg-provider \
  --image kindest/node:v1.36.4 \
  --kubeconfig "$PROVIDER_KUBECONFIG"
kind create cluster --name cnpg-consumer \
  --image kindest/node:v1.36.4 \
  --kubeconfig "$CONSUMER_KUBECONFIG"
kind get kubeconfig --name cnpg-provider --internal > "$PROVIDER_ACCESS_KUBECONFIG"
chmod 600 "$PROVIDER_ACCESS_KUBECONFIG"
```

This local access kubeconfig grants administrator access. Use scoped credentials
for shared environments. Do not use kind's host kubeconfig as the konnector's
provider credentials: its loopback API address is not reachable from the Pod.

## Install CloudNativePG on the provider

Install CloudNativePG on the provider only. Version 1.30 supports Kubernetes 1.36; you can consult its [support policy](https://cloudnative-pg.io/docs/1.30/supported_releases/) for other environments.

```bash
kubectl --kubeconfig "$PROVIDER_KUBECONFIG" apply --server-side \
  -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.2.yaml
kubectl --kubeconfig "$PROVIDER_KUBECONFIG" -n cnpg-system rollout status \
  deployment/cnpg-controller-manager --timeout=180s
kubectl --kubeconfig "$PROVIDER_KUBECONFIG" label crd clusters.postgresql.cnpg.io \
  core.kbind.io/exported=true --overwrite
```

## Install the konnector on the consumer

Install the published Helm chart with an explicit image repository and tag.
These overrides avoid the older defaults bundled with this release candidate.

```bash
helm upgrade --install konnector oci://ghcr.io/kbind-dev/charts/konnector-v2 \
  --version 2.0.0-rc7 \
  --kubeconfig "$CONSUMER_KUBECONFIG" \
  --namespace kbind --create-namespace \
  --set-string image.repository=ghcr.io/kbind-dev/konnector \
  --set-string image.tag=v2.0.0-rc7 \
  --wait --timeout 180s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n kbind get deployments,pods
```

The chart's default permissions cover arbitrary bound APIs. Review them before
installing in a shared cluster. The controller now runs in Kubernetes and uses
its ServiceAccount to access the consumer API.

## Bind the API

Store the provider access kubeconfig in a consumer Secret, then create the
Connection and ClusterBinding:

```bash
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n kbind create secret generic \
  postgres-provider-kubeconfig --from-file=kubeconfig="$PROVIDER_ACCESS_KUBECONFIG"
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" apply -f - <<'EOF_BINDING'
apiVersion: core.kbind.io/v1alpha1
kind: Connection
metadata:
  name: postgres-provider
spec:
  kubeconfigSecretRef:
    namespace: kbind
    name: postgres-provider-kubeconfig
    key: kubeconfig
  schema:
    source: CRD
---
apiVersion: core.kbind.io/v1alpha1
kind: ClusterBinding
metadata:
  name: checkout-postgres
spec:
  connectionRef:
    name: postgres-provider
  apis:
    - name: clusters.postgresql.cnpg.io
  relatedResources:
    - group: ""
      resource: secrets
      direction: FromProvider
      selector:
        names:
          - checkout-db-app
          - checkout-db-ca
EOF_BINDING
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" wait \
  --for=condition=Ready connection/postgres-provider --timeout=120s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" wait \
  --for=condition=Ready clusterbinding/checkout-postgres --timeout=120s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" wait \
  --for='jsonpath={.status.conditions[?(@.type=="Established")].status}=True' \
  crd/clusters.postgresql.cnpg.io --timeout=120s
```

The binding selects two provider Secrets in one `FromProvider` rule: `checkout-db-app` and `checkout-db-ca`. These are fixed names for this example. Selectors do not follow references in the database spec or restrict the credentials' RBAC permissions.

## Request the database

Request a database on the consumer:

```bash
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" apply -f - <<'EOF_DATABASE'
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: checkout-db
  namespace: default
spec:
  instances: 1
  bootstrap:
    initdb:
      database: checkout
      owner: checkout_app
  storage:
    size: 1Gi
EOF_DATABASE
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default wait \
  --for=condition=Ready clusters.postgresql.cnpg.io/checkout-db --timeout=600s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default get \
  clusters.postgresql.cnpg.io/checkout-db
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default wait \
  --for=create secret/checkout-db-app secret/checkout-db-ca --timeout=120s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default get \
  secret checkout-db-app checkout-db-ca
```

CloudNativePG creates the `checkout` database, the `checkout_app` role, and its password on the provider. The konnector returns status and the selected Secrets to the consumer. PostgreSQL runs on the provider; no CloudNativePG operator is installed on the consumer. One instance is sufficient for this demonstration but does not provide high availability.

## Connect from the consumer

kbind does not connect cluster networks. For the optional local kind setup, expose the primary through a provider NodePort and route a consumer Service to that node. This assumes both kind nodes share the Docker `kind` network. For other environments, use your provider's reachable database endpoint and an appropriate network policy.

```bash
kubectl --kubeconfig "$PROVIDER_KUBECONFIG" apply -f - <<'EOF_PROVIDER_SERVICE'
apiVersion: v1
kind: Service
metadata:
  name: checkout-db-local
  namespace: default
spec:
  type: NodePort
  selector:
    cnpg.io/cluster: checkout-db
    role: primary
  ports:
    - name: postgres
      port: 5432
      targetPort: 5432
      nodePort: 30432
EOF_PROVIDER_SERVICE
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" apply -f - <<'EOF_CONSUMER_SERVICE'
apiVersion: v1
kind: Service
metadata:
  name: checkout-db-rw
  namespace: default
spec:
  ports:
    - name: postgres
      port: 5432
      targetPort: 30432
EOF_CONSUMER_SERVICE
export PROVIDER_NODE_IP=$(docker inspect cnpg-provider-control-plane | \
  python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["NetworkSettings"]["Networks"]["kind"]["IPAddress"])')
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" apply -f - <<EOF_ENDPOINT
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: checkout-db-provider
  namespace: default
  labels:
    kubernetes.io/service-name: checkout-db-rw
    endpointslice.kubernetes.io/managed-by: kbind-local-example
addressType: IPv4
ports:
  - name: postgres
    protocol: TCP
    port: 30432
endpoints:
  - addresses: ["$PROVIDER_NODE_IP"]
    conditions:
      ready: true
EOF_ENDPOINT
```

The consumer Service uses `checkout-db-rw`, the provider's primary Service name, so the client can verify the server certificate against its DNS name. The SQL Job below reads the copied password from a Secret and verifies TLS using the copied CA. It creates an `orders` table, inserts a checkout order, and reads it back without printing credentials.

```bash
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" apply -f - <<'EOF_SQL_CHECK'
apiVersion: batch/v1
kind: Job
metadata:
  name: checkout-db-check
  namespace: default
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: psql
          image: postgres:18.1
          env:
            - name: PGHOST
              value: checkout-db-rw
            - name: PGDATABASE
              value: checkout
            - name: PGUSER
              valueFrom:
                secretKeyRef:
                  name: checkout-db-app
                  key: username
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef:
                  name: checkout-db-app
                  key: password
            - name: PGSSLMODE
              value: verify-full
            - name: PGSSLROOTCERT
              value: /etc/postgres-ca/ca.crt
          volumeMounts:
            - name: ca
              mountPath: /etc/postgres-ca
              readOnly: true
          command: [psql]
          args:
            - --no-psqlrc
            - --set=ON_ERROR_STOP=1
            - --command=CREATE TABLE IF NOT EXISTS orders (id text PRIMARY KEY, total_cents integer NOT NULL); INSERT INTO orders VALUES ('launch-day-001', 2499) ON CONFLICT (id) DO UPDATE SET total_cents = EXCLUDED.total_cents; SELECT id, total_cents FROM orders WHERE id = 'launch-day-001';
      volumes:
        - name: ca
          secret:
            secretName: checkout-db-ca
            items:
              - key: ca.crt
                path: ca.crt
EOF_SQL_CHECK
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default wait \
  --for=condition=Complete job/checkout-db-check --timeout=180s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default logs job/checkout-db-check
```

Expected query result:

```text
       id       | total_cents
----------------+-------------
 launch-day-001 |        2499
(1 row)
```

This proves that the provider controller provisioned a usable database and the consumer received working credentials. The Service and EndpointSlice are explicit local networking configuration, not synchronized resources.

## Cleanup

Remove the consumer request while the konnector is running and confirm its provider copy disappears before removing the binding:

```bash
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default delete job checkout-db-check
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n default delete \
  clusters.postgresql.cnpg.io checkout-db
kubectl --kubeconfig "$PROVIDER_KUBECONFIG" -n default wait --for=delete \
  clusters.postgresql.cnpg.io/checkout-db --timeout=180s
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" delete clusterbinding checkout-postgres
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" delete connection postgres-provider
```

After bindings and Connections have finished deleting, uninstall the konnector:

```bash
helm uninstall konnector --namespace kbind --kubeconfig "$CONSUMER_KUBECONFIG"
kubectl --kubeconfig "$CONSUMER_KUBECONFIG" -n kbind delete secret postgres-provider-kubeconfig
```

With the chart's default `installCRDs: true`, uninstalling also removes the core
CRDs. Unbind any other services first if you reused an existing installation.

For the optional kind setup, delete only these test clusters:

```bash
kind delete cluster --name cnpg-consumer
kind delete cluster --name cnpg-provider
```

CloudNativePG's PVC retention policy is separate from deletion of its `Cluster` resource. For retained clusters, inspect PVCs and remove the local networking resources when they are no longer needed. See [Troubleshooting](../troubleshooting.md) for binding and synchronization failures.
