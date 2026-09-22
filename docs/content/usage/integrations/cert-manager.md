---
title: Cert-Manager
description: |
    Guide on integrating kube-bind with cert-manager for automated TLS certificate management.
weight: 10
---

# Cert-Manager Integration

## Setup

The following sections will guide you through the one-time setup that is required for providing certificates using cert-manager and kube-bind.

Follow the [prerequisites](index.md#prerequisites), then select the provider:

```bash
kubectl config use-context provider
```

### Install cert-manager

Install cert-manager in your Kubernetes cluster, where kube-bind backend is running, if you haven't already. You can follow the [official installation guide](https://cert-manager.io/docs/installation/kubernetes/).

### Export the Certificate CRD

To export the cert-manager `Certificate` CRD, add the kube-bind export label to it:

```bash
kubectl label crd certificates.cert-manager.io core.kbind.io/exported=true --overwrite
```

### Create a SelfSigned Issuer

```bash
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: my-selfsigned-issuer
spec:
  selfSigned: {}
EOF
```

### Create a Catalog Export

It's now time to configure kube-bind to export the certificate resource. Create a catalog `Export` for `Certificate` resources like this one:

```bash
kubectl apply -f - <<EOF
apiVersion: catalog.kbind.io/v1alpha1
kind: Export
metadata:
  name: certificate
spec:
  title: Certificate
  description: Manage TLS certificates with cert-manager.
  apis:
    - name: certificates.cert-manager.io
  defaults:
    relatedResources:
      - group: ""
        resource: secrets
        direction: FromProvider
        selector:
          names:
            - my-tls-cert
EOF
```

This walkthrough keeps the original fixed `my-tls-cert` Secret name. v2 related-resource selectors are name/label based, they do not follow `spec.secretName`. If you change the Secret name, update the Export selector to match.

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
kubectl bind export certificate \
  --kubeconfig=./consumer.kubeconfig \
  --install-konnector=false
```

The command applies the bundle for the `certificate` Export to the consumer cluster. It assumes a matching konnector is already installed separately.

```bash
kubectl wait --for=condition=Established crd/certificates.cert-manager.io --timeout=120s
```

### Create a Certificate

Now you can finally create a `Certificate` object in your consumer cluster. The cert-manager in the provider cluster will handle the issuance and management of the TLS certificate.

!!! note
    `my-selfsigned-issuer` must be present in the provider cluster for this example to work.

```bash
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: my-tls-cert
  namespace: default
spec:
  commonName: my-ca
  isCA: true
  issuerRef:
    kind: ClusterIssuer
    name: my-selfsigned-issuer
  secretName: my-tls-cert
EOF
```

### Wait for Provisioning

Observe that the `Certificate` object is created in the consumer cluster and the corresponding TLS Secret is generated and copied through the related-resource rule:

```bash
kubectl --context=consumer -n default get certificates
NAME          READY   SECRET        AGE
my-tls-cert   True    my-tls-cert   6m55s

kubectl --context=consumer -n default get secrets
NAME          TYPE                DATA   AGE
my-tls-cert   kubernetes.io/tls   3      6m33s
```
