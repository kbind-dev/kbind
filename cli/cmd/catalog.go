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

func newCatalogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "List the provider's offerings (Exports and Collections)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := resolveClient()
			if err != nil {
				return err
			}
			catalog, err := client.Catalog(cmd.Context())
			if err != nil {
				return err
			}
			if len(catalog.Exports) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "The catalog is empty.")
				return nil
			}

			collectionsOf := map[string][]string{}
			for _, col := range catalog.Collections {
				for _, name := range col.Exports {
					collectionsOf[name] = append(collectionsOf[name], col.Title)
				}
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTITLE\tAPIS\tCOLLECTIONS")
			for _, e := range catalog.Exports {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					e.Name, e.Title, strings.Join(e.APIs, ","), strings.Join(collectionsOf[e.Name], ","))
			}
			return w.Flush()
		},
	}
}
