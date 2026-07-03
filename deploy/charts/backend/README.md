# backend

A Helm chart for kube-bind backend deployment

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.7.1](https://img.shields.io/badge/AppVersion-v0.7.1-informational?style=flat-square)

## Installation

```bash
helm install kube-bind-backend oci://ghcr.io/kbind-dev/charts/backend --version <version>
```

## Configuration

See [values.yaml](values.yaml) for the full list of configurable parameters.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling |
| autoscaling.enabled | bool | `false` | Enable horizontal pod autoscaling |
| autoscaling.maxReplicas | int | `100` | Maximum number of replicas |
| autoscaling.minReplicas | int | `1` | Minimum number of replicas |
| autoscaling.targetCPUUtilizationPercentage | int | `80` | Target CPU utilization percentage |
| backend.apibindingIgnorePrefixes | list | `[]` | Name prefixes of APIBindings to ignore when generating APIServiceExportTemplates |
| backend.apiexportEndpointSliceName | string | `""` | APIExport EndpointSlice name to watch |
| backend.clusterScopeIsolation | string | `"prefix"` | Cluster-scope isolation mode. Options: none, prefix, namespaced |
| backend.consumerScope | string | `"namespaced"` | Consumer scope. Options: "namespaced" |
| backend.context | string | `""` | The name of the kubeconfig context to use |
| backend.cookieEncryptionKey | string | `""` | Cookie encryption key (base64 encoded). Empty generates random key on each start (not for production!) |
| backend.cookieSigningKey | string | `""` | Cookie signing key (base64 encoded). Empty generates random key on each start (not for production!) |
| backend.externalAddress | string | `""` | External address clients use to reach the backend |
| backend.externalServerName | string | `""` | External server name for TLS SNI |
| backend.extraArgs | list | `[]` | Extra command-line arguments to pass to the backend |
| backend.frontendDisabled | bool | `false` | Disable the frontend UI |
| backend.image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| backend.image.repository | string | `"ghcr.io/kbind-dev/backend"` | Image repository |
| backend.image.tag | string | `""` | Overrides the image tag whose default is the chart appVersion |
| backend.kubeconfig | string | `""` | Path to a kubeconfig file. Only required if out-of-cluster. |
| backend.listenAddress | string | `"0.0.0.0:8080"` | Address the backend listens on |
| backend.loggingLevel | int | `2` | Logging verbosity level |
| backend.multiclusterRuntimeProvider | string | `""` | Multicluster runtime provider (e.g., "kcp") |
| backend.namespacePrefix | string | `"kube-bind-"` | Prefix for namespaces created by kube-bind |
| backend.oidc.allowedGroups | list | `[]` | List of groups allowed to access bindings. With embedded OIDC, system:authenticated is added automatically |
| backend.oidc.allowedUsers | list | `[]` | List of users allowed to access bindings |
| backend.oidc.callbackUrl | string | `""` | OIDC callback URL |
| backend.oidc.clientId | string | `""` | OIDC client ID |
| backend.oidc.clientSecret | string | `""` | Not required for providers using PKCE or public clients |
| backend.oidc.clientSecretKey | string | `""` | Key within the secret (e.g., "client-secret") |
| backend.oidc.clientSecretName | string | `""` | If set, the secret will be mounted as OIDC_CLIENT_SECRET env var |
| backend.oidc.issuerUrl | string | `""` | OIDC issuer URL (leave empty for embedded OIDC server) |
| backend.oidc.type | string | `"embedded"` | OIDC provider type. Options: "embedded" or "external" |
| backend.prettyName | string | `""` | Human-readable name for this backend instance |
| backend.schemaSource | string | `""` | Schema source (e.g., "apiresourceschemas") |
| backend.sessionStorage.redisAddress | string | `""` |  |
| backend.sessionStorage.redisPassword | string | `""` |  |
| backend.tls.certSecretName | string | `""` | Name of the Kubernetes secret containing TLS certificate |
| backend.tls.enabled | bool | `false` | Enable TLS for the backend |
| backend.tls.tlsCertFile | string | `"/etc/kube-bind/tls/tls.crt"` | Path to TLS certificate file inside the container |
| backend.tls.tlsKeyFile | string | `"/etc/kube-bind/tls/tls.key"` | Path to TLS key file inside the container |
| certManager.clusterIssuer | string | `""` | Name of the ClusterIssuer to use |
| certManager.enabled | bool | `false` | Enable cert-manager integration for automatic TLS certificates |
| examples.enabled | bool | `false` | Enable example resources to seed on first start |
| fullnameOverride | string | `""` | Override the full release name |
| gatewayApi.enabled | bool | `false` | Enable Gateway API resources |
| gatewayApi.gateway.annotations | object | `{}` | Annotations to add to the Gateway resource |
| gatewayApi.gateway.className | string | `""` | Gateway class name |
| gatewayApi.gateway.httpPort | int | `80` | HTTP listener port |
| gatewayApi.gateway.httpsPort | int | `443` | HTTPS listener port |
| gatewayApi.gateway.tls.certificateRefs | list | `[]` | TLS certificate references for the Gateway |
| gatewayApi.route.annotations | object | `{}` | Annotations to add to the HTTPRoute resource |
| gatewayApi.route.hostnames | list | `[]` | Hostnames for the HTTPRoute |
| gatewayApi.route.path | string | `"/"` | Path match for the HTTPRoute |
| gatewayApi.route.pathType | string | `"PathPrefix"` | Path match type for the HTTPRoute |
| hostAliases | list | `[]` | Host aliases for /etc/hosts injection into pods |
| imagePullSecrets | list | `[]` | Secrets for pulling images from a private repository |
| initContainers | list | `[]` | Additional init containers |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"http"}}` | Liveness probe configuration |
| nameOverride | string | `""` | Override the chart name |
| nodeSelector | object | `{}` | Node selector for pod scheduling |
| podAnnotations | object | `{}` | Annotations to add to the pod |
| podLabels | object | `{}` | Labels to add to the pod |
| podSecurityContext | object | `{}` | Pod security context |
| rbac.create | bool | `true` | Specifies whether RBAC resources should be created |
| readinessProbe | object | `{"httpGet":{"path":"/healthz","port":"http"}}` | Readiness probe configuration |
| replicaCount | int | `1` | Number of replicas for the backend deployment |
| resources | object | `{}` | Resource requests and limits |
| securityContext | object | `{}` | Container security context |
| service.httpsNodePort | string | `""` | NodePort for HTTPS (only used when type is NodePort) |
| service.httpsPort | int | `8443` | HTTPS service port |
| service.nodePort | string | `""` | NodePort for HTTP (only used when type is NodePort) |
| service.port | int | `8080` | HTTP service port |
| service.type | string | `"ClusterIP"` | Service type |
| serviceAccount.annotations | object | `{}` | Annotations to add to the service account |
| serviceAccount.automount | bool | `true` | Automatically mount the ServiceAccount's API credentials |
| serviceAccount.create | bool | `true` | Specifies whether a service account should be created |
| serviceAccount.name | string | `""` | The name of the service account to use. If not set and create is true, a name is generated using the fullname template |
| tolerations | list | `[]` | Tolerations for pod scheduling |
| volumeMounts | list | `[]` | Additional volumeMounts on the output Deployment definition |
| volumes | list | `[]` | Additional volumes on the output Deployment definition |

---

*This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs). Do not edit manually.*
