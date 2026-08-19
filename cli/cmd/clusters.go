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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/kbind/kbind/backend/gateway/api"
)

func newClustersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clusters",
		Short: "List the consumer clusters connected to this provider",
		Long: `Clusters shows which consumer clusters are heartbeating against the
provider (from the konnector's Leases), what each has bound, and how many
objects it is syncing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := resolveClient()
			if err != nil {
				return err
			}
			clusters, err := client.Clusters(cmd.Context())
			if err != nil {
				return err
			}
			if len(clusters.Clusters) == 0 && len(clusters.Pending) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No consumer clusters yet.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 8, 2, ' ', 0)
			fmt.Fprintln(w, "CLUSTER\tSTATUS\tEXPORT\tIDENTITY\tHEARTBEAT\tSYNCED")
			for _, cluster := range clusters.Clusters {
				for _, b := range cluster.Bindings {
					status := "Live"
					if !b.Live {
						status = "Stale"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
						shortUID(cluster.UID), status, b.Export, b.Identity,
						ago(b.LastHeartbeat), syncedSummary(b.APIs))
				}
			}
			for _, p := range clusters.Pending {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					"-", "NeverConnected", p.Export, p.Identity, "bound "+ago(p.CreatedAt), "-")
			}
			return w.Flush()
		},
	}
}

func shortUID(uid string) string {
	if len(uid) > 13 {
		return uid[:13]
	}
	return uid
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

func syncedSummary(apis []api.SyncedAPI) string {
	parts := make([]string, 0, len(apis))
	for _, a := range apis {
		parts = append(parts, fmt.Sprintf("%s=%d", a.Name, a.SyncedCount))
	}
	return strings.Join(parts, ",")
}
