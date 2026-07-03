/*
Copyright 2025 The Kube Bind Authors.

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
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	logsv1 "k8s.io/component-base/logs/api/v1"

	"github.com/kube-bind/kube-bind/cli/pkg/help"
	"github.com/kube-bind/kube-bind/cli/pkg/kubectl/dev/plugin"
)

var (
	devCreateExampleUses = `
	# Create a development environment with kind cluster and default OCI chart
	%[1]s create

	# Create a development environment with custom provider cluster name
	%[1]s create --provider-cluster-name my-provider

	# Create with custom chart path (local development)
	%[1]s create --chart-path ../deploy/charts/backend

	# Create with specific chart version
	%[1]s create --chart-version 0.1.0

	# Create with custom OCI chart
	%[1]s create --chart-path oci://registry.example.com/charts/backend --chart-version 1.0.0
	`
)

func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Manage development environment for kube-bind",
		Long: help.Doc(`
		Manage a development environment for kube-bind using kind clusters.

		This command provides subcommands to initialize and manage kind clusters
		configured for kube-bind development.
		`),
		SilenceUsage: true,
	}

	// Add create subcommand
	createCmd, err := newCreateCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(createCmd)

	// Add delete subcommand
	deleteCmd, err := newDeleteCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(deleteCmd)

	exampleCmd, err := newExampleCommand(streams)
	if err != nil {
		return nil, err
	}

	cmd.AddCommand(exampleCmd)

	return cmd, nil
}

func newCreateCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create development environment with kind cluster and kube-bind backend",
		Long: help.Doc(`
		Create a complete development environment for kube-bind using kind clusters.

		This command will:

		- Create a kind cluster configured for kube-bind development
		- Add kube-bind.dev.local to /etc/hosts (with sudo prompts if needed)
		- Install kube-bind backend helm chart (default: OCI chart from ghcr.io)
		- Configure necessary port mappings (8443, 15021)

		The backend chart can be sourced from:

		- OCI registry (default): oci://ghcr.io/kbind-dev/charts/backend
		- Local filesystem: --chart-path ./deploy/charts/backend
		- Custom OCI registry: --chart-path oci://custom.registry/charts/backend
		`),
		Example:      help.Examplesf(devCreateExampleUses, "kubectl bind dev"),
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := logsv1.ValidateAndApply(opts.Logs, nil); err != nil {
				return err
			}

			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			return opts.Run(cmd.Context())
		},
	}
	opts.AddCmdFlags(cmd)

	return cmd, nil
}

func newDeleteCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete development environment",
		Long: help.Doc(`
		Delete the development environment for kube-bind.

		This command will delete the kind cluster created for kube-bind development.
		`),
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := logsv1.ValidateAndApply(opts.Logs, nil); err != nil {
				return err
			}

			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			return opts.RunDelete()
		},
	}
	opts.AddCmdFlags(cmd)

	return cmd, nil
}

func newExampleCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:   "example",
		Short: "Get example manifest for MangoDB",
		Long: help.Doc(`
		This will print example CRD you can use for development environment testing.
		`),
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := logsv1.ValidateAndApply(opts.Logs, nil); err != nil {
				return err
			}

			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			return opts.RunPrintExample()
		},
	}
	opts.AddCmdFlags(cmd)

	return cmd, nil
}
