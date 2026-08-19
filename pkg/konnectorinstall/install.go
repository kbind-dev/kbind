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

// Package konnectorinstall installs the konnector (CRDs, RBAC, Deployment)
// into a consumer cluster — the CLI's --install-konnector and the gateway's
// browser-apply use it. The embedded CRDs are synced from sdk/config/crd by
// `make codegen`.
package konnectorinstall

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/kbind/kbind/pkg/kubeapply"
)

//go:embed manifests
var manifests embed.FS

// DefaultImage is the konnector image installed when none is specified.
const DefaultImage = "ghcr.io/kbind/konnector:latest"

// Manifests returns the full konnector install (CRDs first, then namespace,
// RBAC and Deployment) as one YAML multi-doc with the image resolved.
func Manifests(image string) ([]byte, error) {
	if image == "" {
		image = DefaultImage
	}
	var out bytes.Buffer
	paths := []string{}
	if err := fs.WalkDir(manifests, "manifests", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return nil, err
	}
	// CRDs sort before the konnector manifest (crds/ subdirectory), which is
	// exactly the order we want.
	sort.Strings(paths)
	for _, path := range paths {
		data, err := manifests.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if out.Len() > 0 {
			out.WriteString("\n---\n")
		}
		out.Write(bytes.ReplaceAll(data, []byte("__KONNECTOR_IMAGE__"), []byte(image)))
	}
	return out.Bytes(), nil
}

// Objects returns the konnector install decoded to unstructured objects.
func Objects(image string) ([]*unstructured.Unstructured, error) {
	data, err := Manifests(image)
	if err != nil {
		return nil, err
	}
	return kubeapply.DecodeYAML(data)
}

// Apply installs/upgrades the konnector in the cluster behind cfg.
func Apply(ctx context.Context, cfg *rest.Config, image string) ([]string, error) {
	objs, err := Objects(image)
	if err != nil {
		return nil, err
	}
	applier, err := kubeapply.New(cfg, "kbind-installer")
	if err != nil {
		return nil, err
	}
	applied, err := applier.Apply(ctx, objs)
	if err != nil {
		return applied, fmt.Errorf("installing konnector: %w", err)
	}
	return applied, nil
}
