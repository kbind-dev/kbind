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

// Command backend is the kbind provider-side service layer: one binary with
// module flags. Gateway (HTTP API + UI), issuer (Grant provisioning), reaper
// (Lease-keyed GC) and browser-apply are each independently switchable; the
// GitOps-only core path needs none of them.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kbind/kbind/backend/auth"
	"github.com/kbind/kbind/backend/gateway"
	"github.com/kbind/kbind/backend/issuer"
	"github.com/kbind/kbind/backend/reaper"
	"github.com/kbind/kbind/pkg/konnectorinstall"
	"github.com/kbind/kbind/pkg/servicecrds"
	catalogv1alpha1 "github.com/kbind/kbind/sdk/apis/catalog/v1alpha1"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
	iamv1alpha1 "github.com/kbind/kbind/sdk/apis/iam/v1alpha1"
	"github.com/kbind/kbind/web"
)

// version is stamped via -ldflags at release time.
var version = "dev"

var scheme = runtime.NewScheme()

func init() {
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		corev1alpha1.AddToScheme,
		catalogv1alpha1.AddToScheme,
		iamv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
}

type options struct {
	// Modules.
	enableGateway bool
	enableIssuer  bool
	enableReaper  bool
	enableApply   bool

	// CRD self-install.
	installCRDs bool

	// Gateway.
	listenAddr     string
	providerName   string
	externalURL    string
	pickupTTL      time.Duration
	konnectorImage string

	// Issuer.
	externalAddress string
	externalCAFile  string
	issuerScope     string

	// Sessions.
	cookieSigningKey    string
	cookieEncryptionKey string
	sessionTTL          time.Duration

	// OIDC (kube-apiserver/kcp style).
	oidcIssuerURL     string
	oidcClientID      string
	oidcClientSecret  string
	oidcCAFile        string
	oidcUsernameClaim string
	oidcGroupsClaim   string
	oidcScopes        string
	oidcRedirectURL   string
	oidcMock          bool
	oidcMockListen    string

	// kubernetes auth.
	kubernetesAuth bool

	// Reaper.
	reaperTTL      time.Duration
	reaperRevoke   bool
	reaperDelete   bool
	reaperInterval time.Duration

	// Manager (issuer/reaper controllers).
	metricsAddr      string
	probeAddr        string
	leaderElect      bool
	leaderElectionID string
}

func main() {
	var o options
	flag.BoolVar(&o.enableGateway, "enable-gateway", true, "serve the HTTP API (catalog, bind, bundle pickup) and UI")
	flag.BoolVar(&o.enableIssuer, "enable-issuer", true, "run the provider-side controllers (Grant provisioning, catalog validation)")
	flag.BoolVar(&o.enableReaper, "enable-reaper", false, "run the Lease-keyed GC of stale Grants")
	flag.BoolVar(&o.enableApply, "enable-apply", false,
		"enable POST /api/apply (browser-apply): consumer kubeconfigs transit the gateway when on")
	flag.BoolVar(&o.installCRDs, "install-crds", true,
		"install/refresh the service-layer CRDs (catalog.kbind.io, iam.kbind.io) at startup")

	flag.StringVar(&o.listenAddr, "listen-address", ":8080", "address the gateway listens on")
	flag.StringVar(&o.providerName, "provider-name", "kbind", "human-facing provider name")
	flag.StringVar(&o.externalURL, "external-url", "", "externally reachable base URL of this gateway (defaults callback URLs; e.g. https://kbind.example.com)")
	flag.DurationVar(&o.pickupTTL, "pickup-ttl", 5*time.Minute, "lifetime of one-time bundle pickup URLs")
	flag.StringVar(&o.konnectorImage, "konnector-image", konnectorinstall.DefaultImage, "konnector image installed by browser-apply")

	flag.StringVar(&o.externalAddress, "external-address", "", "provider API server URL written into issued kubeconfigs (default: this process's API server host)")
	flag.StringVar(&o.externalCAFile, "external-ca-file", "", "CA bundle for issued kubeconfigs (default: the SA token's ca.crt)")
	flag.StringVar(&o.issuerScope, "issuer-scope", string(issuer.ScopeCluster),
		"RBAC reach of issued credentials: Cluster (exported resources in all namespaces) or Namespace (boundary namespace only)")

	flag.StringVar(&o.cookieSigningKey, "cookie-signing-key", "", "base64 HMAC key for session tokens (32/64 bytes; shared across replicas; empty = ephemeral random, dev only)")
	flag.StringVar(&o.cookieEncryptionKey, "cookie-encryption-key", "", "base64 AES key for session tokens (16/24/32 bytes; shared across replicas; empty = ephemeral random, dev only)")
	flag.DurationVar(&o.sessionTTL, "session-ttl", 12*time.Hour, "session token lifetime")

	flag.StringVar(&o.oidcIssuerURL, "oidc-issuer-url", "", "OIDC issuer URL")
	flag.StringVar(&o.oidcClientID, "oidc-client-id", "", "OIDC client ID")
	flag.StringVar(&o.oidcClientSecret, "oidc-client-secret", "", "OIDC client secret")
	flag.StringVar(&o.oidcCAFile, "oidc-ca-file", "", "CA bundle for the OIDC issuer's TLS")
	flag.StringVar(&o.oidcUsernameClaim, "oidc-username-claim", "sub", "ID-token claim used as the stable username")
	flag.StringVar(&o.oidcGroupsClaim, "oidc-groups-claim", "", "ID-token claim holding group membership")
	flag.StringVar(&o.oidcScopes, "oidc-scopes", "openid,profile,email", "comma-separated OAuth2 scopes")
	flag.StringVar(&o.oidcRedirectURL, "oidc-redirect-url", "", "externally reachable OIDC callback URL (default: <external-url>/api/auth/oidc/callback)")
	flag.BoolVar(&o.oidcMock, "oidc-mock", false, "run an embedded mock OIDC issuer that auto-approves every login (dev only)")
	flag.StringVar(&o.oidcMockListen, "oidc-mock-listen", "",
		"fixed listen address for the mock issuer (e.g. 127.0.0.1:5556 — port-forwardable when running in-cluster; empty = random port)")
	flag.BoolVar(&o.kubernetesAuth, "kubernetes-auth", false,
		"also accept provider-cluster bearer tokens, verified via TokenReview (for in-platform callers that already hold a cluster identity)")

	flag.DurationVar(&o.reaperTTL, "reaper-ttl", 30*time.Minute, "heartbeat silence after which a Grant is considered stale")
	flag.BoolVar(&o.reaperRevoke, "reaper-revoke", false, "delete stale Grants (revokes their credentials)")
	flag.BoolVar(&o.reaperDelete, "reaper-delete-boundary", false,
		"when revoking, also delete the boundary namespace and the synced objects inside it (destructive; requires --reaper-revoke)")
	flag.DurationVar(&o.reaperInterval, "reaper-interval", time.Minute, "reaper sweep cadence")

	flag.StringVar(&o.metricsAddr, "metrics-bind-address", ":8086", "address the metric endpoint binds to")
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8082", "address the health/readiness probe endpoint binds to")
	flag.BoolVar(&o.leaderElect, "leader-elect", false, "enable leader election for the provider-side controllers")
	flag.StringVar(&o.leaderElectionID, "leader-election-id", "backend.kbind.io", "name of the Lease used for leader election")

	zapOpts := zap.Options{Development: true}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	if err := run(o); err != nil {
		ctrl.Log.Error(err, "backend exited with error")
		os.Exit(1)
	}
}

func run(o options) error {
	ctx := ctrl.SetupSignalHandler()
	log := ctrl.Log.WithName("backend")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}

	// Self-install the service-layer CRDs so a fresh provider (and the plain
	// `go run ./cmd/backend` dev loop) works without a chart install first.
	if o.installCRDs {
		if err := servicecrds.Install(ctx, cfg); err != nil {
			return fmt.Errorf("installing service-layer CRDs (disable with --install-crds=false): %w", err)
		}
		log.Info("service-layer CRDs installed")
	}

	g, ctx := errgroup.WithContext(ctx)

	kubeIssuer := &issuer.KubeIssuer{Scope: issuer.Scope(o.issuerScope)}

	// Provider-side controllers (issuer, catalog validation, reaper) run on a
	// manager; the gateway is a plain HTTP server with a direct client.
	if o.enableIssuer || o.enableReaper {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                        scheme,
			Metrics:                       metricsserver.Options{BindAddress: o.metricsAddr},
			HealthProbeBindAddress:        o.probeAddr,
			LeaderElection:                o.leaderElect,
			LeaderElectionID:              o.leaderElectionID,
			LeaderElectionReleaseOnCancel: true,
		})
		if err != nil {
			return err
		}
		if o.enableIssuer {
			kubeIssuer.Client = mgr.GetClient()
			if err := (&issuer.GrantReconciler{Issuer: kubeIssuer}).SetupWithManager(mgr); err != nil {
				return err
			}
			if err := (&issuer.ExportReconciler{}).SetupWithManager(mgr); err != nil {
				return err
			}
		}
		if o.enableReaper {
			reapIssuer := kubeIssuer
			if reapIssuer.Client == nil {
				reapIssuer.Client = mgr.GetClient()
			}
			if err := mgr.Add(&reaper.Reaper{
				Client:         mgr.GetClient(),
				Issuer:         reapIssuer,
				TTL:            o.reaperTTL,
				Revoke:         o.reaperRevoke,
				DeleteBoundary: o.reaperDelete,
				Interval:       o.reaperInterval,
			}); err != nil {
				return err
			}
		}
		g.Go(func() error { return ignoreCanceled(mgr.Start(ctx)) })
	}

	if o.enableGateway {
		server, err := buildGateway(ctx, o, cfg)
		if err != nil {
			return err
		}
		httpServer := &http.Server{
			Addr:              o.listenAddr,
			Handler:           server.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		g.Go(func() error {
			log.Info("gateway listening", "address", o.listenAddr)
			if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		g.Go(func() error {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return httpServer.Shutdown(shutdownCtx)
		})
	}

	log.Info("starting kbind backend", "version", version,
		"gateway", o.enableGateway, "issuer", o.enableIssuer, "reaper", o.enableReaper, "apply", o.enableApply)
	return g.Wait()
}

// buildGateway wires sessions, authenticators, the bundle builder and the
// HTTP server.
func buildGateway(ctx context.Context, o options, cfg *rest.Config) (*gateway.Server, error) {
	signingKey, err := decodeKeyFlag(o.cookieSigningKey, "--cookie-signing-key")
	if err != nil {
		return nil, err
	}
	encryptionKey, err := decodeKeyFlag(o.cookieEncryptionKey, "--cookie-encryption-key")
	if err != nil {
		return nil, err
	}
	if len(signingKey) == 0 {
		ctrl.Log.Info("WARNING: no --cookie-signing-key set; sessions are ephemeral and will not survive restarts or work across replicas")
	}
	sessions := auth.NewSessions(signingKey, encryptionKey, o.sessionTTL)

	externalURL := strings.TrimSuffix(o.externalURL, "/")
	if externalURL == "" {
		externalURL = "http://localhost" + o.listenAddr // dev fallback for :8080-style addrs
	}

	oidcCfg := auth.OIDCConfig{
		IssuerURL:     o.oidcIssuerURL,
		ClientID:      o.oidcClientID,
		ClientSecret:  o.oidcClientSecret,
		CAFile:        o.oidcCAFile,
		UsernameClaim: o.oidcUsernameClaim,
		GroupsClaim:   o.oidcGroupsClaim,
		Scopes:        splitNonEmpty(o.oidcScopes),
		RedirectURL:   o.oidcRedirectURL,
	}
	if oidcCfg.RedirectURL == "" {
		oidcCfg.RedirectURL = externalURL + "/api/auth/oidc/callback"
	}
	if o.oidcMock {
		mockCfg, err := auth.StartMockOIDC(ctx, oidcCfg.RedirectURL, o.oidcMockListen)
		if err != nil {
			return nil, err
		}
		ctrl.Log.Info("WARNING: --oidc-mock is on; every login auto-approves as the mock user (dev only)", "issuer", mockCfg.IssuerURL)
		mockCfg.RedirectURL = oidcCfg.RedirectURL
		oidcCfg = mockCfg
	}
	oidcAuth, err := auth.NewOIDC(ctx, oidcCfg, sessions)
	if err != nil {
		return nil, err
	}
	authenticators := []auth.Authenticator{oidcAuth}
	if o.kubernetesAuth {
		k8sAuth, err := auth.NewKubernetes(cfg)
		if err != nil {
			return nil, err
		}
		authenticators = append(authenticators, k8sAuth)
	}

	providerClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}

	externalAddress := o.externalAddress
	if externalAddress == "" {
		externalAddress = cfg.Host
	}
	var externalCA []byte
	if o.externalCAFile != "" {
		externalCA, err = os.ReadFile(o.externalCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading --external-ca-file: %w", err)
		}
	}

	return gateway.New(gateway.Config{
		ProviderName:   o.providerName,
		Version:        version,
		ApplyEnabled:   o.enableApply,
		PickupTTL:      o.pickupTTL,
		KonnectorImage: o.konnectorImage,
	}, providerClient, &issuer.BundleBuilder{
		Client:          providerClient,
		ExternalAddress: externalAddress,
		ExternalCA:      externalCA,
	}, authenticators, web.Static()), nil
}

func decodeKeyFlag(value, flagName string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be base64: %w", flagName, err)
	}
	return key, nil
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
