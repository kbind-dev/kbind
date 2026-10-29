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

package backend

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/kube-bind/kube-bind/backend/auth"
	"github.com/kube-bind/kube-bind/backend/controllers/bindableresourcesrequest"
	"github.com/kube-bind/kube-bind/backend/controllers/cluster"
	"github.com/kube-bind/kube-bind/backend/controllers/clusterbinding"
	"github.com/kube-bind/kube-bind/backend/controllers/serviceexport"
	"github.com/kube-bind/kube-bind/backend/controllers/serviceexportrbac"
	"github.com/kube-bind/kube-bind/backend/controllers/serviceexportrequest"
	"github.com/kube-bind/kube-bind/backend/controllers/servicenamespace"
	http "github.com/kube-bind/kube-bind/backend/http"
	kube "github.com/kube-bind/kube-bind/backend/kubernetes"
	"github.com/kube-bind/kube-bind/backend/provider/kcp/controllers/apibindingtemplate"
	"github.com/kube-bind/kube-bind/backend/provider/kcp/controllers/apiresourceschema"
	"github.com/kube-bind/kube-bind/backend/provider/kcp/controllers/vwrbac"
	"github.com/kube-bind/kube-bind/backend/session/memory"
	"github.com/kube-bind/kube-bind/backend/session/redis"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
)

type Server struct {
	Config *Config

	OIDC       *auth.OIDCServiceProvider
	oidcOnce   sync.Once
	oidcErr    error
	Kubernetes *kube.Manager
	WebServer  *http.Server

	Controllers
}

type Controllers struct {
	ServiceExportRBAC        *serviceexportrbac.APIServiceExportRBACReconciler
	ClusterBinding           *clusterbinding.ClusterBindingReconciler
	ServiceExport            *serviceexport.APIServiceExportReconciler
	ServiceExportRequest     *serviceexportrequest.APIServiceExportRequestReconciler
	ServiceNamespace         *servicenamespace.APIServiceNamespaceReconciler
	Cluster                  *cluster.ClusterReconciler
	BindableResourcesRequest *bindableresourcesrequest.BindableResourcesRequestReconciler

	// Provider specific controllers - only set when provider is "kcp"
	APIBindingTemplate *apibindingtemplate.APIBindingTemplateReconciler
	APIResourceSchema  *apiresourceschema.APIResourceSchemaReconciler
	VWRBAC             *vwrbac.VWRBACReconciler
}

func NewServer(ctx context.Context, c *Config) (*Server, error) {
	logger := klog.FromContext(ctx)

	s := &Server{
		Config: c,
	}

	// Parse konnector host aliases from flag
	var konnectorHostAliases []corev1.HostAlias
	for _, entry := range c.Options.KonnectorHostAlias {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			konnectorHostAliases = append(konnectorHostAliases, corev1.HostAlias{
				IP:        parts[0],
				Hostnames: strings.Split(parts[1], ","),
			})
		}
	}

	var err error
	s.Kubernetes, err = kube.NewKubernetesManager(
		ctx,
		c.Options.NamespacePrefix,
		c.Options.PrettyName,
		c.ExternalAddressGenerator,
		kubebindv1alpha2.InformerScope(c.Options.ExtraOptions.ConsumerScope),
		c.Options.ExternalCA,
		c.Options.TLSExternalServerName,
		s.Config.Manager,
		c.Options.OIDC.Type == string(kubebindv1alpha2.OIDCProviderTypeEmbedded),
		konnectorHostAliases,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up Kubernetes Manager: %w", err)
	}

	// Always create the web server for health check endpoints
	s.WebServer, err = http.NewServer(c.Options.Serve)
	if err != nil {
		return nil, fmt.Errorf("error setting up HTTP Server: %w", err)
	}

	if c.Options.FrontendDisabled {
		logger.Info("Frontend is disabled; running in backend only mode")
	} else {
		// setup oidc backend
		callback := c.Options.OIDC.CallbackURL
		if callback == "" {
			callback = fmt.Sprintf("http://%s/api/callback", s.WebServer.Addr().String())
		}

		// Use lazy initialization for embedded OIDC to avoid circular dependency
		if c.Options.OIDC.OIDCServer != nil {
			logger.Info("Using embedded OIDC server; will initialize lazily")
		} else {
			// External OIDC provider - initialize immediately
			s.OIDC, err = s.initializeOIDCProvider(ctx, callback)
			if err != nil {
				return nil, fmt.Errorf("error setting up OIDC: %w", err)
			}
		}

		signingKey, err := base64.StdEncoding.DecodeString(c.Options.Cookie.SigningKey)
		if err != nil {
			return nil, fmt.Errorf("error creating signing key: %w", err)
		}

		var encryptionKey []byte
		if c.Options.Cookie.EncryptionKey != "" {
			var err error
			encryptionKey, err = base64.StdEncoding.DecodeString(c.Options.Cookie.EncryptionKey)
			if err != nil {
				return nil, fmt.Errorf("error creating encryption key: %w", err)
			}
		}

		sessionStore := memory.New()

		if addr := c.Options.Session.Redis.Address; addr != "" {
			redisStore, err := redis.New(addr, c.Options.Session.Redis.Password)
			if err != nil {
				return nil, fmt.Errorf("error setting up Redis session store: %w", err)
			}
			sessionStore = redisStore
		}

		handler, err := http.NewHandler(
			s,
			s.Config.Options.OIDC.OIDCServer,
			c.Options.OIDC.AuthorizeURL,
			callback,
			c.Options.PrettyName,
			c.Options.TestingAutoSelect,
			signingKey,
			encryptionKey,
			c.Options.SchemaSource,
			kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
			s.Kubernetes,
			c.Options.Frontend,
			c.Options.TokenExpiry,
			sessionStore,
		)
		if err != nil {
			return nil, fmt.Errorf("error setting up HTTP Handler: %w", err)
		}
		if err := handler.AddRoutes(s.WebServer.Router); err != nil {
			return nil, fmt.Errorf("error adding routes to HTTP Server: %w", err)
		}
	}

	opts := controller.TypedOptions[mcreconcile.Request]{
		SkipNameValidation: ptr.To(c.Options.TestingSkipNameValidation),
	}

	s.ClusterBinding, err = clusterbinding.NewClusterBindingReconciler(
		ctx,
		s.Config.Manager,
		opts,
		kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up ClusterBinding Controller: %w", err)
	}

	// Register the ClusterBinding controller with the manager
	if err := s.ClusterBinding.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up ClusterBinding controller with manager: %w", err)
	}

	s.ServiceExportRBAC, err = serviceexportrbac.NewAPIServiceExportRBACReconciler(
		ctx,
		s.Config.Manager,
		kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
		opts,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up RBAC Controller: %w", err)
	}
	// Register the APIServiceExport RBAC controller with the manager
	if err := s.ServiceExportRBAC.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up RBAC controller with manager: %w", err)
	}

	// construct APIServiceExport controller with multicluster-runtime
	s.ServiceExport, err = serviceexport.NewAPIServiceExportReconciler(
		ctx,
		s.Config.Manager,
		kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
		opts,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up APIServiceExport Controller: %w", err)
	}

	// Register the APIServiceExport controller with the manager
	if err := s.ServiceExport.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up APIServiceExport controller with manager: %w", err)
	}

	s.ServiceNamespace, err = servicenamespace.NewAPIServiceNamespaceReconciler(
		ctx,
		s.Config.Manager,
		opts,
		kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
		kubebindv1alpha2.Isolation(c.Options.Isolation),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up APIServiceNamespace Controller: %w", err)
	}

	// Register the APIServiceNamespace controller with the manager
	if err := s.ServiceNamespace.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up APIServiceNamespace controller with manager: %w", err)
	}

	s.ServiceExportRequest, err = serviceexportrequest.NewAPIServiceExportRequestReconciler(
		ctx,
		s.Config.Manager,
		opts,
		kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
		kubebindv1alpha2.Isolation(c.Options.Isolation),
		c.Options.SchemaSource,
		c.Options.SchemaSyncInterval,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up ServiceExportRequest Controller: %w", err)
	}
	if err := s.ServiceExportRequest.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up ServiceExportRequest controller with manager: %w", err)
	}

	s.Cluster, err = cluster.NewClusterReconciler(
		ctx,
		s.Config.Manager,
		opts,
		c.Options.OIDC.AllowedGroups,
		c.Options.OIDC.AllowedUsers,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up LogicalCluster Controller: %w", err)
	}
	if err := s.Cluster.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up LogicalCluster controller with manager: %w", err)
	}

	// Setup BindableResourcesRequest controller - this handles direct CRD-based binding
	// when the frontend is disabled (no HTTP API/OIDC flow)
	if c.Options.FrontendDisabled {
		s.BindableResourcesRequest, err = bindableresourcesrequest.NewBindableResourcesRequestReconciler(
			ctx,
			s.Config.Manager,
			opts,
			kubebindv1alpha2.InformerScope(c.Options.ConsumerScope),
			kubebindv1alpha2.Isolation(c.Options.Isolation),
			s.Kubernetes,
		)
		if err != nil {
			return nil, fmt.Errorf("error setting up BindableResourcesRequest Controller: %w", err)
		}
		if err := s.BindableResourcesRequest.SetupWithManager(s.Config.Manager); err != nil {
			return nil, fmt.Errorf("error setting up BindableResourcesRequest controller with manager: %w", err)
		}
		logger.Info("BindableResourcesRequest controller enabled for CRD-based binding")
	}

	// Setup kcp-specific controllers. Only active when provider is kcp
	// (APIBindings and APIResourceSchemas only exist in kcp workspaces).
	if c.Options.Provider == "kcp" {
		s.APIBindingTemplate, err = apibindingtemplate.New(ctx, s.Config.Manager, opts, c.Options.ProviderKcp.APIBindingIgnorePrefixes, c.ClientConfig, c.Scheme)
		if err != nil {
			return nil, fmt.Errorf("error setting up APIBindingTemplate Controller: %w", err)
		}
		if err := s.APIBindingTemplate.SetupWithManager(s.Config.Manager); err != nil {
			return nil, fmt.Errorf("error setting up APIBindingTemplate controller with manager: %w", err)
		}
		logger.Info("APIBindingTemplate controller enabled for kcp provider")

		s.APIResourceSchema, err = apiresourceschema.New(ctx, s.Config.Manager, opts, c.ClientConfig, c.Scheme)
		if err != nil {
			return nil, fmt.Errorf("error setting up APIResourceSchema Controller: %w", err)
		}
		if err := s.APIResourceSchema.SetupWithManager(s.Config.Manager); err != nil {
			return nil, fmt.Errorf("error setting up APIResourceSchema controller with manager: %w", err)
		}
		logger.Info("APIResourceSchema controller enabled for kcp provider")

		s.VWRBAC, err = vwrbac.New(ctx, s.Config.Manager, opts, c.ClientConfig)
		if err != nil {
			return nil, fmt.Errorf("error setting up VWRBAC Controller: %w", err)
		}
		if err := s.VWRBAC.SetupWithManager(s.Config.Manager); err != nil {
			return nil, fmt.Errorf("error setting up VWRBAC controller with manager: %w", err)
		}
		logger.Info("VWRBAC controller enabled (temporary; bootstraps consumer-workspace RBAC for kcp apiresourceschema VW)")
	}

	return s, nil
}

func (s *Server) initializeOIDCProvider(ctx context.Context, callback string) (*auth.OIDCServiceProvider, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Initializing OIDC Service Provider", "issuerURL", s.Config.Options.OIDC.IssuerURL)

	clientID := s.Config.Options.OIDC.IssuerClientID
	clientSecret := s.Config.Options.OIDC.IssuerClientSecret
	issuerURL := s.Config.Options.OIDC.IssuerURL
	tlsConfig := s.Config.Options.OIDC.TLSConfig

	if clientSecret != "" {
		logger.Info("WARNING: OIDC client secret is configured. For CLI applications, consider using PKCE.")
	}

	if tlsConfig != nil {
		return auth.NewOIDCServiceProviderWithTLS(
			ctx,
			clientID,
			clientSecret,
			callback,
			issuerURL,
			tlsConfig,
		)
	}

	return auth.NewOIDCServiceProvider(
		ctx,
		clientID,
		clientSecret,
		callback,
		issuerURL,
	)
}

func (s *Server) ensureOIDC(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	if s.Config.Options.OIDC.OIDCServer == nil {
		// External OIDC - already initialized
		return nil
	}

	s.oidcOnce.Do(func() {
		callback := s.Config.Options.OIDC.CallbackURL
		if callback == "" {
			callback = fmt.Sprintf("http://%s/api/callback", s.WebServer.Addr().String())
		}

		s.OIDC, s.oidcErr = s.initializeOIDCProvider(ctx, callback)
		if s.oidcErr == nil {
			logger.Info("Successfully initialized embedded OIDC provider")
		}
	})
	return s.oidcErr
}

func (s *Server) GetOIDCProvider(ctx context.Context) (*auth.OIDCServiceProvider, error) {
	if err := s.ensureOIDC(ctx); err != nil {
		return nil, err
	}
	return s.OIDC, nil
}

func (s *Server) Addr() net.Addr {
	if s.WebServer == nil {
		return nil
	}
	return s.WebServer.Addr()
}

func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	// start controller-runtime manager after bootstrap completes
	go func() {
		if err := s.Config.Manager.Start(ctx); err != nil {
			logger.Error(err, "Failed to start controller manager")
		}
	}()

	go func() {
		// When using native k8s provider/singelton cluster - provider is set to nil
		// so we need to seed the default cluster. If provider is set - it is provider responsibility
		// to seed the default cluster in each managed cluster either via provider machinery or via provider
		// wrapper (see kcp example).
		if s.Config.Provider == nil {
			if err := s.seedCluster(ctx); err != nil {
				logger.Error(err, "Failed to seed default cluster")
			}
		}
	}()

	return s.WebServer.Start(ctx)
}

func (s *Server) seedCluster(ctx context.Context) error {
	return s.Kubernetes.SeedDefaultCluster(ctx)
}
