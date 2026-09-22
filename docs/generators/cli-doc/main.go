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

package main

import (
	"fmt"
	"html"
	"strings"

	"github.com/spf13/cobra"

	bindcmd "github.com/kbind/kbind/cli/cmd"
)

func main() {
	root := bindcmd.New()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()
	fmt.Println("# CLI Reference")
	fmt.Println("\nGenerated from this checkout's CLI commands. Examples use the `kubectl bind` plugin. See [plugin installation](../../setup/kubectl-plugin.md).")
	fmt.Println("\nBundle output contains provider credentials: encrypt it or use a secret manager, never commit plaintext to git. Override `--konnector-image` with an explicit v2 image, the compiled `latest` default is not a v2 guarantee.")
	render(root)
}

func render(command *cobra.Command) {
	fmt.Printf("\n## kubectl %s\n\n", command.CommandPath())
	description := command.Long
	if description == "" {
		description = command.Short
	}
	fmt.Println(formatDescription(description))
	if command.Example != "" {
		fmt.Printf("\n```bash\n%s\n```\n", strings.TrimSpace(command.Example))
	}
	command.InitDefaultHelpFlag()
	fmt.Printf("\n```text\n%s```\n", command.UsageString())
	for _, child := range command.Commands() {
		if !child.Hidden {
			render(child)
		}
	}
}

func formatDescription(description string) string {
	var paragraphs []string
	for _, paragraph := range strings.Split(strings.Trim(description, "\n"), "\n\n") {
		indent := paragraph[:len(paragraph)-len(strings.TrimLeft(paragraph, " \t"))]
		if len(indent) >= 2 || strings.Contains(indent, "\t") {
			lines := strings.Split(paragraph, "\n")
			for i := range lines {
				lines[i] = strings.TrimPrefix(lines[i], indent)
			}
			paragraphs = append(paragraphs, "```bash\n"+strings.Join(lines, "\n")+"\n```")
		} else {
			paragraphs = append(paragraphs, html.EscapeString(strings.Join(strings.Fields(paragraph), " ")))
		}
	}
	return strings.Join(paragraphs, "\n\n")
}
