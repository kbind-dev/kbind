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

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kbind/kbind/backend/gateway/api"
)

// ErrLoginRequired asks the user to run bind login.
var ErrLoginRequired = errors.New("not logged in to this gateway — run: kubectl bind login <server-url>")

// Client talks to one gateway.
type Client struct {
	// Server is the gateway base URL.
	Server string
	// Token is the session bearer token (empty = unauthenticated).
	Token string

	HTTP *http.Client
}

// NewClient builds a gateway client.
func NewClient(server, token string) *Client {
	return &Client{
		Server: server,
		Token:  token,
		HTTP:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusUnauthorized {
		return ErrLoginRequired
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		var apiErr api.Error
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return errors.New(apiErr.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Provider fetches gateway metadata (no auth needed).
func (c *Client) Provider(ctx context.Context) (*api.Provider, error) {
	var p api.Provider
	if err := c.do(ctx, http.MethodGet, "/api/provider", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Catalog fetches the visible exports and collections.
func (c *Client) Catalog(ctx context.Context) (*api.Catalog, error) {
	var cat api.Catalog
	if err := c.do(ctx, http.MethodGet, "/api/catalog", nil, &cat); err != nil {
		return nil, err
	}
	return &cat, nil
}

// Clusters fetches the consumer clusters known from konnector heartbeats.
func (c *Client) Clusters(ctx context.Context) (*api.Clusters, error) {
	var clusters api.Clusters
	if err := c.do(ctx, http.MethodGet, "/api/clusters", nil, &clusters); err != nil {
		return nil, err
	}
	return &clusters, nil
}

// Instances fetches the provider-side objects synced under one catalog item.
func (c *Client) Instances(ctx context.Context, export string) (*api.ExportInstances, error) {
	var instances api.ExportInstances
	if err := c.do(ctx, http.MethodGet, "/api/catalog/"+url.PathEscape(export)+"/instances", nil, &instances); err != nil {
		return nil, err
	}
	return &instances, nil
}

// Bind requests a binding and returns the pickup handle.
func (c *Client) Bind(ctx context.Context, export string) (*api.BindResponse, error) {
	var res api.BindResponse
	if err := c.do(ctx, http.MethodPost, "/api/bind", api.BindRequest{Export: export}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Pickup redeems a one-time pickup URL and returns the literal YAML bundle.
func (c *Client) Pickup(ctx context.Context, pickupURL string) ([]byte, error) {
	u, err := url.Parse(pickupURL)
	if err != nil {
		return nil, err
	}
	full := pickupURL
	if !u.IsAbs() {
		full = c.Server + pickupURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/yaml")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		var apiErr api.Error
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return nil, errors.New(apiErr.Error)
		}
		return nil, fmt.Errorf("bundle pickup: %s", res.Status)
	}
	return data, nil
}
