---
description: >
  Setup kind cluster for local kube-bind development and testing.
---

# kind Setup

This guide walks you through setting up a local Kubernetes cluster using [kind](https://kind.sigs.k8s.io/) for kube-bind development and testing.

## Prerequisites

- [kind](https://kind.sigs.k8s.io/) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl) installed


## Setup Steps

We will need 2 kind clusters (or more) to simulate service provider and consumer clusters. 
We will use experimental support for dedicated networking between kind clusters to allow direct communication.

```bash
# Create provider cluster
export KIND_EXPERIMENTAL_DOCKER_NETWORK=kube-bind
kind create cluster --name provider 
kubectl cluster-info --context kind-provider

helm upgrade \
    --install \
    --namespace kube-bind \
    --create-namespace \
    kube-bind oci://ghcr.io/kbind-dev/charts/backend --version 0.0.0-a50df39d7e4c71f7808f4209ec23f294c5ac8f86

helm upgrade \
    --install \
    --namespace kube-bind \
    --create-namespace \
    --set image.repository=ghcr.io/mjudeikis/kube-bind/backend \
    --set image.tag=c27e82a \
    --set examples.enabled=true \
    kube-bind ./deploy/charts/backend