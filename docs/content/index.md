# kbind Documentation

## Overview

kbind (formerly known as kube-bind) is a project that aims to provide better support for service providers and consumers that reside in distinct Kubernetes clusters. We are actively working towards a stable release, and welcome feedback from the community.

This documentation covers **v2**. The [0.x documentation](https://docs.kbind.dev/latest/) remains the default. See [Moving from 0.x](usage/migration.md) before upgrading an existing installation.

![High-level architecture diagram](images/high-level.png)

The diagram illustrates provider/consumer separation. In v2, authentication through an identity provider is optional, and namespace isolation must be arranged by the deployment rather than automatic namespace remapping.

- A service provider defines its API contract in terms of CRDs and credential RBAC, and labels the CRDs for export.
- Service consumers identify the services they want to consume using the optional catalog, CLI or Web UI, or apply core bindings directly through GitOps.
- The service CRDs get installed in the service consumer clusters, with objects of the defined kinds written and read by the service consumers.
- The service provider indirectly reads and writes those objects as the interface to the service that it provides.
- The service provider does not inject controllers/operators into the service consumer's cluster.
- A single vendor-neutral, OpenSource agent - `konnector` per consumer cluster connects it with the requested services.

The v2 core consumes a kubeconfig Secret, a `Connection`, and `ClusterBinding` or namespaced `Binding` objects. The backend is optional, and the shipped syncer preserves resource scope, namespace, and name.

## v2 Architecture

```mermaid
flowchart LR
  subgraph Consumer
    B["Secret + Connection + bindings"]
    K[Konnector]
    C[Custom resources]
    B --> K
    C --> K
  end
  subgraph Provider
    P[Custom resources]
    O[Service operator]
    G["Optional backend: catalog, auth, issuer, reaper"]
    P <--> O
  end
  K -- "spec up" --> P
  P -- "status down" --> K
  K --> C
  G -. "one-apply bundle" .-> B
```

The optional backend produces the bundle, synchronization runs directly between the consumer's konnector and the provider API server, not through the gateway. See [Architecture Overview](developers/architecture.md) for the controller layout.

## Getting Started

- **[Quickstart](setup/quickstart.md)** - Get up and running with kbind quickly
- **[Setup Guide](setup/index.md)** - Complete installation and deployment options
- **[Usage Guide](usage/index.md)** - Learn the core concepts and APIs

## Contributing

We ❤️ our contributors! If you're interested in helping us out, please head over to our [Contributing](contributing/index.md) guide.

## Getting in touch

There are several ways to communicate with us:

- The [`#kbind-dev` channel](https://kubernetes.slack.com/archives/C046PRXNJ4W) in the [Kubernetes Slack workspace](https://slack.k8s.io).
- Our mailing lists:
    - [kube-bind-dev](https://groups.google.com/g/kube-bind-dev) for development discussions.
- Our bi-weekly community meetings, every second Thursday at 11am EST (5pm CET).
    - By joining the [kube-bind-dev mailing list](https://groups.google.com/g/kube-bind-dev), you should receive an invite.
    - See our [community meeting notes document](https://docs.google.com/document/d/1qztpKOmdZu5iWq_4N9n3AZpcAPuPhBiGNbje5GPg0iM) for upcoming and past agendas.
    <!-- TODO(community-call-advertise): once the CNCF community page is registered, add a sub-bullet linking to https://community.cncf.io/kube-bind/ -->
    <!-- TODO(community-call-advertise): once a YouTube channel is set up, add a sub-bullet linking to recordings. -->

See the [community page](community/index.md) for more details.
