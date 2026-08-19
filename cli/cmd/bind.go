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

package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kbind/kbind/backend/issuer"
	"github.com/kbind/kbind/pkg/konnectorinstall"
	"github.com/kbind/kbind/pkg/kubeapply"
)

// newExportCmd binds a catalog Export: `kubectl bind export <name>` fetches
// the one-apply bundle and applies it (or prints it with -o yaml).
func newExportCmd() *cobra.Command {
	var (
		output           string
		kubeconfig       string
		installKonnector bool
		konnectorImage   string
	)
	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Bind a catalog Export: fetch the one-apply bundle and apply it",
		Long: `Bind requests credentials for an Export, picks up the one-apply bundle
(Secret + Connection + ClusterBinding) and applies it to your current cluster.

With -o yaml the bundle is printed instead of applied — commit it to git and
your GitOps pipeline is the client (the credentials inside stay valid until
the provider revokes the grant).`,
		Args: cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		client, _, err := resolveClient()
		if err != nil {
			return err
		}

		res, err := client.Bind(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		bundle, err := client.Pickup(cmd.Context(), res.PickupURL)
		if err != nil {
			return err
		}

		if output == "yaml" {
			_, err = cmd.OutOrStdout().Write(bundle)
			return err
		}
		if output != "" {
			return fmt.Errorf("unsupported output format %q (only yaml)", output)
		}

		cfg, err := consumerRestConfig(kubeconfig)
		if err != nil {
			return err
		}
		if installKonnector {
			if _, err := konnectorinstall.Apply(cmd.Context(), cfg, konnectorImage); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "konnector installed")
		}
		applied, err := applyBundle(cmd.Context(), cfg, bundle, !installKonnector)
		if err != nil {
			return err
		}
		for _, a := range applied {
			fmt.Fprintf(cmd.OutOrStdout(), "applied %s\n", a)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "bound %s — watch it: kubectl get connections,clusterbindings\n", args[0])
		return nil
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "print the bundle instead of applying (yaml)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "consumer cluster kubeconfig (default: usual kubectl resolution)")
	cmd.Flags().BoolVar(&installKonnector, "install-konnector", true, "install/upgrade the konnector before applying the bundle")
	cmd.Flags().StringVar(&konnectorImage, "konnector-image", konnectorinstall.DefaultImage, "konnector image to install")
	return cmd
}

// consumerRestConfig resolves the consumer cluster the way kubectl would.
func consumerRestConfig(explicit string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if explicit != "" {
		rules.ExplicitPath = explicit
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// applyBundle applies the YAML bundle; ensureNamespace also creates the
// konnector namespace (needed when the konnector install was skipped).
func applyBundle(ctx context.Context, cfg *rest.Config, bundle []byte, ensureNamespace bool) ([]string, error) {
	objs, err := kubeapply.DecodeYAML(bundle)
	if err != nil {
		return nil, err
	}
	if ensureNamespace {
		objs = append([]*unstructured.Unstructured{kubeapply.NamespaceObject(issuer.KonnectorNamespace)}, objs...)
	}
	applier, err := kubeapply.New(cfg, "kbind-cli")
	if err != nil {
		return nil, err
	}
	return applier.Apply(ctx, objs)
}
