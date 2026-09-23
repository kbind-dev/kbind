---
title: Usage Guide
description: |
  Comprehensive guide to using kube-bind APIs, concepts, and workflows for service providers and consumers.
weight: 200
---

# kube-bind Usage Guide

This section provides comprehensive documentation on how to use kube-bind's core APIs and concepts. Whether you're a service provider looking to export APIs or a consumer wanting to bind to services, this guide covers the essential workflows and components.

## Core Concepts

kube-bind operates on three fundamental concepts:

### Service Provider

The cluster that **exports** APIs and resources, making them available for other clusters to consume. Service providers label exported CRDs, grant credential RBAC, and optionally publish catalog Exports.

### Service Consumer

The cluster that **imports** and uses APIs from service providers. Consumers apply a Connection and bindings directly, or obtain a bundle through the optional backend.

### Konnector Agent

The component that establishes and maintains the secure connection between provider and consumer clusters, synchronizing resources and handling permissions.

## Key API Types

### Connection

**Purpose**: References provider credentials, discovers APIs, and controls schema delivery. **Used by**: Service consumers **Scope**: Cluster-scoped on the consumer

### ClusterBinding and Binding

**Purpose**: Select the APIs whose instances should synchronize. **Used by**: Service consumers **Scope**: Cluster-wide or one consumer namespace

### Export and Collection

**Purpose**: Describe and group offerings in the optional catalog. **Used by**: Service providers **Scope**: Cluster-scoped on the provider

### Grant

**Purpose**: Record issued credentials and resolved APIs. **Used by**: The optional backend's gateway and issuer **Scope**: Cluster-scoped on the provider

## Documentation Structure

### [API Concepts](api-concepts.md)

Deep dive into the core API types, their relationships, and how they work together in the kube-bind ecosystem.

### [Catalog and Grants](catalog.md)

Publish offerings, issue credentials, and inspect connected consumers.

## Common Workflows

### For Service Providers

1. **Export APIs** with CRD labels and RBAC, optionally describing them as catalog Exports.
2. **Implement service** to act on the synced/bound objects so it can be returned to the consumer/user.

### For Service Consumers

1. **Authenticate** to the kube-bind backend
2. **Discover available Exports** through the web UI or CLI
3. **Request a bundle** for a specific Export and apply it to the consumer
4. **Use imported APIs** in their local cluster

The core-only alternative is to supply credentials and bindings directly through [GitOps](gitops.md), with no backend login.

### For Platform Operators

1. **Deploy the konnector** on the consumer and the optional backend on the provider
2. **Configure authentication** and security policies
3. **Monitor connections** and resource synchronization

## Getting Started

If you're new to kube-bind:

1. **Start with the [Quickstart Guide](../setup/quickstart.md)** for a hands-on introduction
2. **Review [API Concepts](api-concepts.md)** to understand the fundamental types
3. **Check the [Reference Documentation](../reference/index.md)** for complete API specifications

The konnector agents establish a secure, authenticated connection that allows:

- **API schema synchronization** from provider to consumer
- **Spec up / status down** resource data flow
- **Selected Secret and ConfigMap synchronization**
- **Provider access** governed by credential RBAC

The stock syncer does not rename namespaces or convert resource scope. Review [synchronization](synchronization.md) when designing a shared provider.
