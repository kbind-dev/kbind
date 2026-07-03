---
description: >
  Install kube-bind on an existing Kubernetes cluster via the official Helm chart.
---

# Installation with Helm

kube-bind can be installed on an existing Kubernetes cluster using the official Helm OCI charts.
The backend chart is available as an OCI image for service providers, with konnector charts coming soon for service consumers.

## Quick Start

## Prerequisites & Setup Guides

The following prerequisites are required. Click the links below for detailed setup instructions:

- **[Kubernetes cluster](#kubernetes-cluster)** - A running Kubernetes cluster
- **[Helm 3.x](#helm)** - Package manager for Kubernetes
- **[cert-manager](#cert-manager-setup)** - For TLS certificate management
- **[OIDC provider](#oidc-provider-setup)** - For authentication (Dex, Keycloak, etc.)
- **[Gateway API](#gateway-api-setup)** - (Optional) For advanced ingress management

### Install kube-bind Backend

1. **Get the latest chart version:**

   Visit the [releases page](https://github.com/kube-bind/kube-bind/releases) or check available versions:

   ```bash
   # For latest tag version (recommended for production):
   VERSION=$(curl -s https://api.github.com/repos/kube-bind/kube-bind/releases/latest | grep '"tag_name"' | cut -d'"' -f4 | sed 's/v//')

   # Or use a specific development version:
   # VERSION=0.0.0-9fd9281e661c0d9a426a941111d3d8b08019ebc1
   ```

2. **Configure your values:**

   Edit `deploy/charts/backend/examples/values-local-development.yaml` and replace the placeholder values:
   - `### REPLACE ME ###` with your actual OIDC credentials
   - Update hostnames to match your setup

3. **Install the backend using OCI chart:**

!!! note
    To install production configuration, you will need to have an OIDC provider.
    For more information, just check out the [quickstart guide](./quickstart.md).

```bash
   # Using latest release version
helm upgrade \
      --install \
      --namespace kube-bind \
      --create-namespace \
      --set certManager.enabled=true \
      --set certManager.clusterIssuer=letsencrypt-prod \
      --set backend.oidc.issuerUrl=https://auth.example.com \
      --set backend.oidc.clientId=platform-mesh \
      --set backend.oidc.clientSecret=<client-secret-from-oidc-provider> \
      --set backend.oidc.callbackUrl=https://kube-bind.example.com/api/callback \
      --set gatewayApi.enabled=true \
      --set gatewayApi.gateway.className=nginx \
      --set gatewayApi.gateway.httpsPort=443 \
      --set 'gatewayApi.gateway.tls.certificateRefs[0].name=backend-tls-cert' \
      --set 'gatewayApi.route.hostnames[0]=kube-bind.example.com' \
      --set gatewayApi.route.path=/ \
      --set gatewayApi.route.pathType=PathPrefix \
      --set image.tag=v${VERSION} \
      kube-bind oci://ghcr.io/kbind-dev/charts/backend --version ${VERSION}
```

4. **Seed with example resources (optional):**

```bash
kubectl apply -f deploy/examples/crd-foo.yaml
kubectl apply -f deploy/examples/crd-mangodb.yaml
kubectl apply -f deploy/examples/template-foo.yaml
kubectl apply -f deploy/examples/template-mangodb.yaml
kubectl apply -f deploy/examples/collection.yaml
```

That's it! Your kube-bind backend is now ready to use.

---

### Helm

Install Helm 3.x from [https://helm.sh/docs/intro/install/](https://helm.sh/docs/intro/install/)

**Note**: Helm 3.8+ is required for OCI chart support. Enable experimental OCI support if needed:

```bash
export HELM_EXPERIMENTAL_OCI=1
```

### Gateway API Setup

Install gateway API CRDs and controller for advanced ingress management. Kube-bind supports Gateway API for routing traffic to the backend service.

Follow the official Gateway API installation instructions:
https://gateway-api.sigs.k8s.io/guides/

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.0/standard-install.yaml
```

We used NGINX Gateway controller for testing. Install it as follows:

```bash
helm upgrade --install ngf oci://ghcr.io/nginx/charts/nginx-gateway-fabric --create-namespace -n nginx-gateway
```

### cert-manager Setup

Install cert-manager for automatic TLS certificate management:

```bash
helm repo add jetstack https://charts.jetstack.io
helm repo update

helm upgrade \
  --install \
  --namespace cert-manager \
  --create-namespace \
  --version v1.18.2 \
  --set crds.enabled=true \
  --atomic \
  cert-manager jetstack/cert-manager
```

#### Configure Let's Encrypt (Optional)

Create a ClusterIssuer for automatic certificate provisioning:

```bash
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    # Replace with your email address
    email: admin@example.com
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: le-issuer-account-key
    solvers:
    - dns01:
        cloudflare:
          email: admin@example.com
          apiKeySecretRef:
            name: cloudflare-api-key-secret
            key: api-key
EOF
```

Create the Cloudflare API key secret:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-api-key-secret
  namespace: cert-manager
type: Opaque
data:
  # Replace with your base64 encoded Cloudflare Global API Key
  # Get your API key from: https://dash.cloudflare.com/profile/api-tokens
  # Then encode it: echo -n "your-api-key" | base64
  api-key: #### REPLACE ME WITH BASE64 ENCODED VALUE ####
EOF
```

### OIDC Provider Setup

kube-bind requires an OIDC provider for authentication. Here's how to set up Dex as an example:

#### Install Dex OIDC Provider

```bash
kubectl create namespace oidc

# Request TLS certificate for Dex
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: dex-tls-cert
  namespace: oidc
spec:
  secretName: dex-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
    group: cert-manager.io
  dnsNames:
  - auth.example.com  # Replace with your domain
  usages:
  - digital signature
  - key encipherment
EOF
```

Wait for the certificate to be issued, then install Dex:

```bash
helm repo add dex https://charts.dexidp.io

cat > /tmp/dex-values.yaml <<EOF
config:
  issuer: https://auth.example.com  # Replace with your domain

  logger:
    level: "debug"

  storage:
    type: kubernetes
    config:
      inCluster: true

  web:
    https: 0.0.0.0:5557
    tlsCert: /etc/dex/tls/tls.crt
    tlsKey: /etc/dex/tls/tls.key
    http: "0.0.0.0:5556"

  connectors:
    - type: github
      id: github
      name: GitHub
      config:
        clientID: ### REPLACE ME ###
        clientSecret: ### REPLACE ME ###
        redirectURI: https://auth.example.com/callback
        org: your-org  # Replace with your GitHub org

  staticClients:
    - id: kube-bind
      redirectURIs:
      - https://auth.example.com/callback
      - http://localhost:8000
      - https://kube-bind.example.com/api/callback  # Replace with your domain
      name: 'KubeBindApp'
      secret: ### REPLACE ME ###

service:
  type: LoadBalancer
  ports:
    https:
      port: 443
    http:
      port: 5556

https:
  enabled: true

volumes:
- name: tls-cert
  secret:
    secretName: dex-tls

volumeMounts:
- name: tls-cert
  mountPath: /etc/dex/tls
  readOnly: true
EOF

helm upgrade -i dex dex/dex \
  --create-namespace \
  --namespace oidc \
  -f /tmp/dex-values.yaml
```

---

## Configuration

The example values file at `deploy/charts/backend/examples/values-local-development.yaml` contains all the configuration options. Make sure to replace all `### REPLACE ME ###` placeholders with your actual values:

- **OIDC credentials**: Get these from your OIDC provider (Dex, GitHub, etc.)
- **Cookie keys**: Generate with `openssl rand -base64 32`
- **Hostnames**: Update to match your actual domains
- **Redis Shared Session Storage**: If running multiple replicas of `backend`, provide connection details for `backend.sessionStorage.redisAddress` and `backend.sessionStorage.redisPassword`. The password will be securely injected as the `REDIS_PASSWORD` environment variable.

For production deployments, create your own values file based on the example.

---

## Available OCI Charts

kube-bind Helm charts are published as OCI images to GitHub Container Registry:

### Backend Chart

- **Registry**: `oci://ghcr.io/kbind-dev/charts/backend`
- **Latest Release**: Use the latest tag version (e.g., `1.0.0`)
- **Development Builds**: Available as `0.0.0-<git-sha>` format for each commit to main

### Finding Available Versions

**Release versions:**

```bash
# List all releases
curl -s https://api.github.com/repos/kube-bind/kube-bind/releases | grep '"tag_name"' | head -5

# Get latest release version
VERSION=$(curl -s https://api.github.com/repos/kube-bind/kube-bind/releases/latest | grep '"tag_name"' | cut -d'"' -f4 | sed 's/v//')
echo "Latest version: ${VERSION}"
```
