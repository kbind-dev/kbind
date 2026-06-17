/*
Copyright 2026 The Kube Bind Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package crdpull centralizes how a provider CRD is installed on the consumer:
// stripping it to a consumer-installable form, creating it, and (per
// updatePolicy) updating it when the provider schema changes. Shared by the
// Connection (pullPolicy: All) and the binding (pullPolicy: Bound).
package crdpull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

// AnnotationSchemaHash records the hash of the installed schema so updatePolicy
// can detect provider changes without re-deriving.
const AnnotationSchemaHash = "core.kbind.io/schema-hash"

// Options control how the CRD is reconciled on the consumer.
type Options struct {
	// Create installs the CRD if absent (pullPolicy Bound/All). When false
	// (pullPolicy None) an absent CRD is left to the user; an existing one is
	// only stamped with the managed/connection markers so sync can find it.
	Create bool
	// Update re-applies the CRD spec when the provider schema changes
	// (updatePolicy Always). When false (updatePolicy Once) the schema is pinned.
	Update bool
}

// Pull reconciles the consumer CRD for crdName from the provider. It returns the
// schema hash and whether the CRD is installed on the consumer.
func Pull(ctx context.Context, consumer client.Client, provider client.Reader, crdName, connName string, opts Options) (hash string, installed bool, err error) {
	var remoteCRD apiextensionsv1.CustomResourceDefinition
	if err := provider.Get(ctx, client.ObjectKey{Name: crdName}, &remoteCRD); err != nil {
		return "", false, fmt.Errorf("reading provider CRD: %w", err)
	}
	desired := ForConsumer(&remoteCRD, connName)
	return install(ctx, consumer, desired, connName, opts)
}

// Install installs an already-built (e.g. OpenAPI-synthesized) consumer CRD,
// stamping the managed/connection/schema-hash markers. updatePolicy: Always
// follows changes; Once pins.
func Install(ctx context.Context, consumer client.Client, crd *apiextensionsv1.CustomResourceDefinition, connName string, update bool) (hash string, err error) {
	desired := crd.DeepCopy()
	if desired.Labels == nil {
		desired.Labels = map[string]string{}
	}
	desired.Labels[corev1alpha1.LabelManaged] = "true"
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	desired.Annotations[corev1alpha1.AnnotationConnection] = connName
	h, _, err := install(ctx, consumer, desired, connName, Options{Create: true, Update: update})
	return h, err
}

// install reconciles a fully-built consumer CRD (create-if-absent, update on
// schema change per opts.Update, stamp-only when opts.Create is false).
func install(ctx context.Context, consumer client.Client, desired *apiextensionsv1.CustomResourceDefinition, connName string, opts Options) (string, bool, error) {
	hash := Hash(desired)
	desired.Annotations[AnnotationSchemaHash] = hash

	var existing apiextensionsv1.CustomResourceDefinition
	getErr := consumer.Get(ctx, client.ObjectKey{Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(getErr):
		if !opts.Create {
			return hash, false, nil // None + absent: user manages it
		}
		if err := consumer.Create(ctx, desired); err != nil {
			return "", false, fmt.Errorf("creating consumer CRD: %w", err)
		}
		return hash, true, nil

	case getErr != nil:
		return "", false, getErr

	default:
		if !opts.Create {
			// None + present: only stamp the markers so the syncer can find it.
			if stampMarkers(&existing, connName) {
				if err := consumer.Update(ctx, &existing); err != nil {
					return "", false, fmt.Errorf("stamping consumer CRD: %w", err)
				}
			}
			return hash, true, nil
		}
		if opts.Update && existing.Annotations[AnnotationSchemaHash] != hash {
			existing.Spec = desired.Spec
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[AnnotationSchemaHash] = hash
			existing.Annotations[corev1alpha1.AnnotationConnection] = connName
			if existing.Labels == nil {
				existing.Labels = map[string]string{}
			}
			existing.Labels[corev1alpha1.LabelManaged] = "true"
			if err := consumer.Update(ctx, &existing); err != nil {
				return "", false, fmt.Errorf("updating consumer CRD: %w", err)
			}
		}
		return hash, true, nil
	}
}

// ForConsumer strips a provider CRD to something installable on the consumer
// without a conversion webhook: single storage/served version, conversion
// forced to None, no webhook/caBundle, no ownerRefs/status. It is marked managed
// and annotated with the source Connection.
func ForConsumer(in *apiextensionsv1.CustomResourceDefinition, connName string) *apiextensionsv1.CustomResourceDefinition {
	out := in.DeepCopy()
	out.ResourceVersion = ""
	out.UID = ""
	out.ManagedFields = nil
	out.OwnerReferences = nil
	out.Status = apiextensionsv1.CustomResourceDefinitionStatus{}

	var keep *apiextensionsv1.CustomResourceDefinitionVersion
	for i := range out.Spec.Versions {
		v := &out.Spec.Versions[i]
		if v.Storage {
			keep = v
			break
		}
		if keep == nil && v.Served {
			keep = v
		}
	}
	if keep != nil {
		k := *keep
		k.Storage = true
		k.Served = true
		out.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{k}
	}
	out.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{Strategy: apiextensionsv1.NoneConverter}

	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	out.Labels[corev1alpha1.LabelManaged] = "true"
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	out.Annotations[corev1alpha1.AnnotationConnection] = connName
	return out
}

// Hash is a content hash over the consumer CRD spec, so updatePolicy: Always can
// detect provider schema changes.
func Hash(crd *apiextensionsv1.CustomResourceDefinition) string {
	b, err := json.Marshal(crd.Spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func stampMarkers(crd *apiextensionsv1.CustomResourceDefinition, connName string) bool {
	changed := false
	if crd.Labels[corev1alpha1.LabelManaged] != "true" {
		if crd.Labels == nil {
			crd.Labels = map[string]string{}
		}
		crd.Labels[corev1alpha1.LabelManaged] = "true"
		changed = true
	}
	if crd.Annotations[corev1alpha1.AnnotationConnection] != connName {
		if crd.Annotations == nil {
			crd.Annotations = map[string]string{}
		}
		crd.Annotations[corev1alpha1.AnnotationConnection] = connName
		changed = true
	}
	return changed
}
