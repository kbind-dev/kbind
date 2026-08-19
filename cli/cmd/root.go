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

// Package cmd wires the bind CLI commands.
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kbind/kbind/cli"
)

// Version is stamped via -ldflags at release time.
var Version = "dev"

var serverFlag string

// New builds the bind root command. Binding an export IS the root command
// (`kubectl bind <export>`); everything else is a subcommand.
func New() *cobra.Command {
	root := &cobra.Command{
		Use:   "bind",
		Short: "Bind a provider's APIs into your cluster",
		Long: `bind is a thin client over a kbind gateway. Everything it does is
reproducible by hand: binding returns a one-apply YAML bundle that a plain
"kubectl apply -f" (or GitOps) consumes just as well.

  kubectl bind login https://provider.example.com
  kubectl bind connect                 # install the konnector
  kubectl bind catalog
  kubectl bind export mangodb          # bind a service
  kubectl bind export mangodb -o yaml  # GitOps mode: print, don't apply`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&serverFlag, "server", "", "gateway base URL (default: the last login)")
	root.AddCommand(newLoginCmd(), newConnectCmd(), newCatalogCmd(), newExportCmd(), newClustersCmd(), newInstancesCmd())
	return root
}

// resolveClient builds a gateway client from --server / stored config.
func resolveClient() (*cli.Client, *cli.Config, error) {
	cfg, err := cli.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	server := cli.NormalizeServer(serverFlag)
	if server == "" {
		server = cfg.Server
	}
	if server == "" {
		return nil, nil, fmt.Errorf("no gateway configured — run: kubectl bind login <server-url>")
	}
	return cli.NewClient(server, cfg.Tokens[server]), cfg, nil
}
