/*
Copyright 2022 The Kube Bind Authors.

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

package framework

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var (
	repoRoot string
)

func init() {
	_, f, _, _ := runtime.Caller(0)
	repoRoot = filepath.Clean(filepath.Join(f, "..", "..", "..", ".."))
}

// WorkDir returns a test-specific working directory. Each test gets its own subdirectory
// based on the test name, allowing parallel tests to avoid file conflicts.
func WorkDir(t testing.TB) string {
	workDir := os.Getenv("WORK_DIR")
	if workDir == "" {
		workDir = filepath.Join(RepoRoot(), ".kube-bind")
	}

	return filepath.Join(workDir, sanitizeTestName(t.Name()))
}

// RepoRoot returns the root directory of the repository
func RepoRoot() string {
	return repoRoot
}

// sanitizeTestName converts a test name to a valid directory name by replacing
// path separators and other problematic characters.
func sanitizeTestName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")

	return name
}
