---
title: Cert-Manager
description: |
    Guide on integrating kube-bind with cert-manager for automated TLS certificate management.
weight: 10
---

# Cert-Manager Integration

## Setup

The following sections will guide you through the one-time setup that is required for providing
certificates using cert-manager and kube-bind.

### Install cert-manager

Install cert-manager in your Kubernetes cluster, where kube-bind backend is running, if you haven't
already. You can follow the [official installation guide](https://cert-manager.io/docs/installation/kubernetes/).

### Export the Certificate CRD

To export the cert-manager `Certificate` CRD, add the kube-bind export label to it:

```bash
kubectl label crd certificates.cert-manager.io kube-bind.io/exported=true --overwrite
```

### Create a SelfSigned Issuer

```yaml
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: my-selfsigned-issuer
spec:
  selfSigned: {}
EOF
```

### Create a APIServiceExportTemplate

It's now time to configure kube-bind to export the certificate resource. To do so, create a
kube-bind `APIServiceExportTemplate` for `Certificate` resources like this one:

```yaml
kubectl apply -f - <<EOF
apiVersion: kube-bind.io/v1alpha2
kind: APIServiceExportTemplate
metadata:
  labels:
    provider: cert-manager
  name: certificate
spec:
  permissionClaims:
  - group: ""
    resource: secrets
    selector:
      references:
        - resource: certificates
          group: cert-manager.io
          jsonPath:
            name: 'spec.secretName'
  resources:
  - group: cert-manager.io
    resource: certificates
    versions:
    - v1
  scope: Namespaced
EOF
```

## Usage

Now that everything is set up, users can begin to bind to your backend and begin consuming the new
API.

### Login to kube-bind

```bash
kubectl bind login https://kube-bind.example.com
```

### Request a Binding

Request a binding to the `certificate` template created above. This will allow you to create
`Certificate` objects in your consumer cluster.

```bash
# you will get redirected to UI to authenticate and pick the template
kubectl bind
```

### Wait for the Binding to be Established

Once the binding is active, you can create `Certificate` objects in your consumer cluster, and you
will get `Certificate` objects synced from the provider cluster.

```bash
kubectl bind
🌐 Opening kube-bind UI in your browser...
    https://kube-bind.genericcontrolplane.io?redirect_url=....

Browser opened successfully
Waiting for binding completion from UI...
   (Press Ctrl+C to cancel)

Binding completed successfully!
Created kube-bind namespace.
🔒 Created secret kube-bind/kubeconfig-p6mfh for host https://api.kcp-prod.kcp.internal.canary.k8s.ondemand.com:443, namespace kube-bind-dkxkx
🚀 Deploying konnector v0.6.0 to namespace kube-bind with custom image "ghcr.io/kbind-dev/konnector:v0.6.0-rc1".
   Waiting for the ...................
✅ Created APIServiceBinding certificate for 1 resources
Created 1 APIServiceBinding(s):
  - certificate
Resources bound successfully!
```

### Create a Certificate

Now you can finally create a `Certificate` object in your consumer cluster. The cert-manager in the
provider cluster will handle the issuance and management of the TLS certificate.

!!! note
    `my-selfsigned-issuer` must be present in the provider cluster for this example to work.

```yaml
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

Observe that the `Certificate` object is created in the consumer cluster and the corresponding TLS
Secret is generated:

```bash
kubectl get certificates
NAME          READY   SECRET        AGE
my-tls-cert   True    my-tls-cert   6m55s

kubectl get secrets
NAME          TYPE                DATA   AGE
my-tls-cert   kubernetes.io/tls   3      6m33s
```
