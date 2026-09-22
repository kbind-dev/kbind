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

import "testing"

func TestFormatDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "wrapped prose",
			description: "Connect to a provider\nand bind its APIs.",
			want:        "Connect to a provider and bind its APIs.",
		},
		{
			name:        "paragraph spacing",
			description: "\nFirst paragraph.\n\nSecond\nparagraph.\n",
			want:        "First paragraph.\n\nSecond paragraph.",
		},
		{
			name:        "literal placeholders",
			description: "Run kubectl bind export <name>.",
			want:        "Run kubectl bind export &lt;name&gt;.",
		},
		{
			name:        "indented commands",
			description: "Examples:\n\n  kubectl bind catalog\n  kubectl bind export widgets  # keep spacing",
			want:        "Examples:\n\n```bash\nkubectl bind catalog\nkubectl bind export widgets  # keep spacing\n```",
		},
		{
			name:        "tab indented command",
			description: "\tkubectl bind catalog",
			want:        "```bash\nkubectl bind catalog\n```",
		},
		{
			name:        "empty",
			description: "",
			want:        "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDescription(tt.description); got != tt.want {
				t.Errorf("formatDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}
