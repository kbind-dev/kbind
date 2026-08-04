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
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// Login runs the browser login dance: a localhost callback listener catches
// the session token the gateway appends after the OIDC flow finishes. Returns
// the token. When openBrowser is false the URL is only printed.
func Login(ctx context.Context, server string, openBrowser bool, out func(string)) (string, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = listener.Close() }()

	callback := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	loginURL := server + "/api/auth/oidc/login?redirect=" + url.QueryEscape(callback)

	tokens := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h3>Logged in to kbind.</h3>You can close this tab and return to the terminal."))
		tokens <- token
	})}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	out("Opening " + loginURL)
	out("(if the browser does not open, visit the URL manually)")
	if openBrowser {
		_ = browse(ctx, loginURL)
	}

	select {
	case token := <-tokens:
		return token, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("login timed out after 5 minutes")
	}
}

func browse(ctx context.Context, url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", url).Start()
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.CommandContext(ctx, "xdg-open", url).Start()
	}
}
