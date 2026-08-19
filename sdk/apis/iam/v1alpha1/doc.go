/*
Copyright 2026 The kbind Authors.

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

// Package v1alpha1 contains the issuance/identity API for the kbind service
// layer: the Grant kind in the iam.kbind.io group. A Grant is the typed record
// of "identity X was issued credentials Y for export Z" — the anchor for
// revocation, audit and the reaper. Kept out of catalog.kbind.io so that group
// stays purely presentation+defaults. Lives on the provider; the konnector
// never sees it. See docs/proposals/v2-extended.md.
//
// +kubebuilder:object:generate=true
// +groupName=iam.kbind.io
package v1alpha1
