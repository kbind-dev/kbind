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
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kbind/kbind/pkg/konnectorinstall"
)

func newConnectCmd() *cobra.Command {
	var (
		kubeconfig     string
		konnectorImage string
	)
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect a cluster: install/upgrade the konnector",
		Long: `Connect installs the konnector (CRDs, RBAC, Deployment) into your current
cluster — the only kbind component that ever runs on the consumer side. Bind
services afterwards with "kubectl bind export <name>".

Reproducible by hand: curl -fsS <gateway>/api/konnector | kubectl apply -f -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := consumerRestConfig(kubeconfig)
			if err != nil {
				return err
			}
			applied, err := konnectorinstall.Apply(cmd.Context(), cfg, konnectorImage)
			if err != nil {
				return err
			}
			for _, a := range applied {
				fmt.Fprintf(cmd.OutOrStdout(), "applied %s\n", a)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "konnector installed — bind a service: kubectl bind export <name>")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "consumer cluster kubeconfig (default: usual kubectl resolution)")
	cmd.Flags().StringVar(&konnectorImage, "konnector-image", konnectorinstall.DefaultImage, "konnector image to install")
	return cmd
}
