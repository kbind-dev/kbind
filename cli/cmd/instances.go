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

package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newInstancesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "instances [export]",
		Short: "List the objects consumers synced under a catalog item (all items when omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := resolveClient()
			if err != nil {
				return err
			}

			// No export named: sweep the whole catalog.
			exports := args
			if len(exports) == 0 {
				catalog, err := client.Catalog(cmd.Context())
				if err != nil {
					return err
				}
				for _, e := range catalog.Exports {
					exports = append(exports, e.Name)
				}
				if len(exports) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "The catalog is empty.")
					return nil
				}
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			fmt.Fprintln(w, "EXPORT\tAPI\tNAMESPACE\tNAME\tCLUSTER\tIDENTITY\tAGE")
			total := 0
			for _, export := range exports {
				res, err := client.Instances(cmd.Context(), export)
				if err != nil {
					return err
				}
				for _, inst := range res.Instances {
					total++
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						export, inst.API, inst.Namespace, inst.Name, shortUID(inst.ClusterUID), inst.Identity, ago(inst.CreatedAt))
				}
			}
			if total == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Nothing synced under %s yet.\n", strings.Join(exports, ", "))
				return nil
			}
			return w.Flush()
		},
	}
}
