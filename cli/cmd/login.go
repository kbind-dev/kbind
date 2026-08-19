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

	"github.com/kbind/kbind/cli"
)

func newLoginCmd() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login <server-url>",
		Short: "Authenticate against a kbind gateway and cache the session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server := cli.NormalizeServer(args[0])

			// Surface the provider before the dance, so a typo'd URL fails fast.
			probe := cli.NewClient(server, "")
			provider, err := probe.Provider(cmd.Context())
			if err != nil {
				return fmt.Errorf("reaching gateway %s: %w", server, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logging in to %s (%s)\n", provider.Name, server)

			token, err := cli.Login(cmd.Context(), server, !noBrowser, func(msg string) {
				fmt.Fprintln(cmd.OutOrStdout(), msg)
			})
			if err != nil {
				return err
			}

			cfg, err := cli.LoadConfig()
			if err != nil {
				return err
			}
			cfg.Server = server
			cfg.Tokens[server] = token
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in. Try: kubectl bind catalog\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the login URL instead of opening a browser")
	return cmd
}
