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

// Package web embeds the gateway's UI: a dependency-free static SPA that is a
// pure gateway client — it holds no flow state the gateway doesn't have.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Static returns the UI filesystem rooted at the static directory.
func Static() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	return sub
}
