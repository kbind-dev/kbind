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

// Package cli implements the bind CLI: a thin client over the gateway.
// Everything it does is reproducible by hand with curl + kubectl.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the CLI's stored state: the current gateway and a session token
// per gateway.
type Config struct {
	// Server is the current gateway base URL.
	Server string `json:"server,omitempty"`
	// Tokens maps gateway base URLs to session bearer tokens.
	Tokens map[string]string `json:"tokens,omitempty"`
}

// configPath is ~/.config/kbind/config.json (or the platform equivalent).
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kbind", "config.json"), nil
}

// LoadConfig reads the CLI config; a missing file is an empty config.
func LoadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Tokens: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Tokens == nil {
		cfg.Tokens = map[string]string{}
	}
	return &cfg, nil
}

// Save writes the config (0600 — it holds session tokens).
func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// NormalizeServer canonicalizes a gateway URL flag/arg.
func NormalizeServer(server string) string {
	server = strings.TrimSuffix(strings.TrimSpace(server), "/")
	// Tolerate a pasted API URL: the base is the gateway root.
	server = strings.TrimSuffix(server, "/api")
	if server != "" && !strings.Contains(server, "://") {
		server = "https://" + server
	}
	return server
}
