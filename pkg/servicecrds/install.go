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

// Package servicecrds installs the service-layer CRDs (catalog.kbind.io,
// iam.kbind.io) on the provider cluster. The backend applies them at startup
// (--install-crds) so `go run ./cmd/backend` against a fresh cluster just
// works; the Helm chart ships the same files for out-of-band management. The
// embedded copies are synced from sdk/config/crd by `make codegen`.
package servicecrds

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

//go:embed crds
var files embed.FS

// Install server-side-applies the service-layer CRDs and waits for them to be
// established. Idempotent — safe to run on every startup and alongside a Helm
// install of the same CRDs.
func Install(ctx context.Context, cfg *rest.Config) error {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}

	crds, err := load()
	if err != nil {
		return err
	}
	for _, crd := range crds {
		if err := c.Patch(ctx, crd, client.Apply, client.FieldOwner("kbind-backend"), client.ForceOwnership); err != nil {
			return fmt.Errorf("installing CRD %s: %w", crd.Name, err)
		}
	}

	// Wait until the API server actually serves them, so controller watches
	// and gateway reads start clean instead of erroring until the first sync.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, crd := range crds {
		name := crd.Name
		if err := wait.PollUntilContextCancel(waitCtx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
			var current apiextensionsv1.CustomResourceDefinition
			if err := c.Get(ctx, types.NamespacedName{Name: name}, &current); err != nil {
				return false, nil //nolint:nilerr // not found yet — keep polling
			}
			for _, cond := range current.Status.Conditions {
				if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		}); err != nil {
			return fmt.Errorf("waiting for CRD %s to be established: %w", name, err)
		}
	}
	return nil
}

func load() ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var crds []*apiextensionsv1.CustomResourceDefinition
	err := fs.WalkDir(files, "crds", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(data, &crd); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		crds = append(crds, &crd)
		return nil
	})
	return crds, err
}
