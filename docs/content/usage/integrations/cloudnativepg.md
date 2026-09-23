---
title: CloudNativePG
description: |
    Guide on integrating kube-bind with CloudNativePG for automated Postgres database management.
weight: 30
---

# CloudNativePG Integration

This document shows how the [CloudNativePG](https://cloudnative-pg.io/) Postgres database operator can be integrated and provided using kube-bind.

## Setup

The following sections will guide you through the one-time setup that is required for providing Postgres databases using CloudNativePG and kube-bind.

Follow the [prerequisites](index.md#prerequisites), then select the provider:

```bash
kubectl config use-context provider
```

### Install CloudNativePG

Install CloudNativePG in your Kubernetes cluster, where kube-bind backend is running, if you haven't already. Follow the [official installation guide](https://cloudnative-pg.io/docs/1.28/installation_upgrade) in the CloudNativePG documentation. In its simplest form, the installation consists of applying this manifest:

```bash
kubectl apply \
  --server-side \
  --filename https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.28/releases/cnpg-1.28.0.yaml
```

### Export the Cluster CRD

To export the `Cluster` CRD in the provider cluster, add the kube-bind export label to it:

```bash
kubectl label crd clusters.postgresql.cnpg.io core.kbind.io/exported=true --overwrite
```

### Create a Catalog Export

It's now time to configure kube-bind to export the `clusters` resource. Create a catalog `Export` in the provider cluster like this one:

```bash
kubectl apply -f - <<EOF
apiVersion: catalog.kbind.io/v1alpha1
kind: Export
metadata:
  name: pg-clusters
spec:
  title: CloudNativePG clusters
  description: Provision PostgreSQL clusters with CloudNativePG.
  apis:
    - name: clusters.postgresql.cnpg.io
  defaults:
    relatedResources:
      - group: ""
        resource: secrets
        direction: FromConsumer
        selector:
          names:
            - cluster-example-app-credentials
EOF
```

This walkthrough keeps the `cluster-example-app-credentials` Secret name fixed. v2 related-resource selectors are name/label based, they do not follow `spec.bootstrap.initdb.secret.name`.

## Usage

Now that everything is set up, users can begin to bind to your backend and begin consuming the new API.

```bash
kubectl config use-context consumer
```

### Login to kube-bind

```bash
kubectl bind login https://kube-bind.example.com
kubectl bind catalog
```

### Bind the Export

```bash
kubectl bind export pg-clusters \
  --kubeconfig=./consumer.kubeconfig \
  --install-konnector=false
```

The command applies the bundle for the `pg-clusters` Export to the consumer cluster. It assumes a matching konnector is already installed separately.

```bash
kubectl wait --for=condition=Established crd/clusters.postgresql.cnpg.io --timeout=120s
```

!!! note
    v2 keeps namespace and name unchanged. This walkthrough uses `default` in both clusters instead of the old prefixed namespace form from the legacy docs.

### Create a Managed Database

Verify that a `clusters.postgresql.cnpg.io` CRD is synced to the consumer cluster:

```bash
kubectl get crd clusters.postgresql.cnpg.io
NAME                           CREATED AT
clusters.postgresql.cnpg.io    2025-11-27T14:22:18Z
```

Order a new consumer database instance by creating a Postgres cluster in the consumer cluster. We need to provide our own credentials, otherwise the automatically generated credentials on the provider cluster will be inaccessible to consumers.

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: cluster-example-app-credentials
  namespace: default
type: kubernetes.io/basic-auth
data:
  username: bXktYXBwbGljYXRpb24=
  password: c3VwZXItc2VjcjN0LXBhc3N3MHJk

---
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cluster-example
  namespace: default
spec:
  instances: 3
  storage:
    size: 1Gi
  bootstrap:
    initdb:
      owner: my-application
      secret:
        name: cluster-example-app-credentials
EOF
```

### Wait for Provisioning

The kube-bind konnector and the CloudNativePG operator should now be busy provisioning your database. You can observe the provisioned database and connection Secret in the provider cluster, because v2 preserves namespace/name, both resources stay in `default`:

```bash
kubectl --context=provider -n default get clusters

NAME              AGE   INSTANCES   READY   STATUS                     PRIMARY
cluster-example   22m   3           3       Cluster in healthy state   cluster-example-1
```

```bash
kubectl --context=provider -n default get clusters cluster-example -o yaml
```

---

For troubleshooting and more information, see [Troubleshooting](../troubleshooting.md).
