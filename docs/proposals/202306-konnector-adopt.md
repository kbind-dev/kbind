---
type: proposal
title: konnector adopt
status: in-progress
owner: lhaendler
---

* **Owners**
    * @lhaendler
* **Related Tickets**
    * N/A

# Proposal: konnector adopt

## Adoption

What does adoption mean? Adoption means that a consumer cluster creates an object that exists on the provider cluster. Once the konnector creates the object in the consumer cluster, the consumer cluster controls the lifecycle of the object.
There are use cases in which the provider may want to create objects of an APIService before the consumer binds it to their cluster. These objects should be controlled by the consumer cluster, but may be created before the consumer cluster, and thus the konnector exists. Currently, the konnector will assume the objects have been deleted on the consumer cluster. The konnector will delete the objects. The desired behaviour would be that the Konnector recognizes the object has not existed on the consumer cluster, and to create it instead.

## Proposed change

The Konnector adds an annotation `kube-bind.io/bound=true` to resources that have a counterpart in a consumer cluster. If the annotation is present and the consumer object is missing, the object in the provider cluster gets deleted. If the annotation is not present, and the object in the consumer cluster does not exist, but it does in the provider cluster, the konnector will create the resource in the consumer cluster, and add the annotation.

## Use cases

### Creation of resources by external tools

Developers might have a script that sets up a development environment for an application, including the creation of necessary databases and other backing services. These services live on a dev cluster in the cloud, while the developer works on their application locally, for example in a kind cluster.
### Creation before a binding exists
Some resources may be created on the consumer cluster before it is bound, for example via a provider web GUI. If the cluster only becomes available later, consumers should still be able to manage the resources from their cluster.

## Impact on existing use cases

### Expected

Existing objects on the provider cluster will get the new annotation, bindings will remain intact.

### Edge cases

A consumer object is deleted before the new konnector version is rolled out, and annotations don’t exist on the provider cluster yet. The konnector stops before it deletes the object on the provider cluster.
Then, the new version of the konnector starts, which implements adoption logic. It sees an object on the provider cluster, that has no annotation.
In this case the object will be recreated on the consumer side. This is undesired.
 Proposed fix: The connector will add the annotation on all objects reconciled, but the creation of missing objects on the consumer side needs to be enabled via a feature flag. This way, the feature flag can get activated once all provider objects are updated.
## Alternatives

### Opt in annotation

The default behaviour is not changed, instead the adoption logic requires an annotation on the object in the provider cluster. For example, the annotation could be `kube-bind.io/autoadopt=true`. If the annotation is present, the connector will create the object in the consumer cluster. Once the resource is created the konnector removes the annotation.

### Additional Controller for adoption

Instead of adding the logic to the konnectors existing controllers, a new controller could be added that adopts resources from the provider cluster. This controller would only run once on binding creation (or konnector startup), and would adopt new resources before the other controllers of the konnector are started. This functionality could also be switch-able via a feature flag. A downside of this approach would be that adoption only works on first binding (or konnector startup), which would leave the option to adopt new resources at a later point.

