/*
Copyright 2026 The Kbind Authors.

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

// Package v1alpha1 contains the catalog API for the kbind service layer:
// the Export and Collection kinds in the catalog.kbind.io group. The catalog
// is curation on top of the core's raw discovery — human-facing metadata and
// defaults that turn "a list of CRD names" into "a service you'd choose".
// These CRDs live on the provider and are read only by the gateway/UI/CLI;
// the konnector never sees them. See docs/proposals/v2-extended.md.
//
// +kubebuilder:object:generate=true
// +groupName=catalog.kbind.io
package v1alpha1
