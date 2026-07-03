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

package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	componentbaseversion "k8s.io/component-base/version"
	"k8s.io/klog/v2"

	"github.com/kube-bind/kube-bind/backend/auth"
	"github.com/kube-bind/kube-bind/backend/client"
	"github.com/kube-bind/kube-bind/backend/kubernetes"
	kuberesources "github.com/kube-bind/kube-bind/backend/kubernetes/resources"
	"github.com/kube-bind/kube-bind/backend/oidc"
	"github.com/kube-bind/kube-bind/backend/session"
	"github.com/kube-bind/kube-bind/backend/spaserver"
	bindversion "github.com/kube-bind/kube-bind/pkg/version"
	kubebindv1alpha2 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha2"
	"github.com/kube-bind/kube-bind/web"
)

// See https://developers.google.com/web/fundamentals/performance/optimizing-content-efficiency/http-caching?hl=en
var noCacheHeaders = map[string]string{
	"Expires":         time.Unix(0, 0).Format(time.RFC1123),
	"Cache-Control":   "no-cache, no-store, must-revalidate, max-age=0",
	"X-Accel-Expires": "0", // https://www.nginx.com/resources/wiki/start/topics/examples/x-accel/
}

// writeErrorResponse writes a structured error response to the HTTP response writer
func writeErrorResponse(w http.ResponseWriter, statusCode int, code, message, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResponse := kubebindv1alpha2.NewError(code, message, details)
	if err := json.NewEncoder(w).Encode(errorResponse); err != nil {
		// Fallback to plain text if JSON encoding fails
		http.Error(w, message, statusCode)
	}
}

// mapErrorToCode maps common errors to structured error codes
func mapErrorToCode(err error) (statusCode int, code string, details string) {
	if apierrors.IsNotFound(err) {
		return http.StatusNotFound, kubebindv1alpha2.ErrorCodeResourceNotFound, err.Error()
	}
	if apierrors.IsUnauthorized(err) {
		return http.StatusUnauthorized, kubebindv1alpha2.ErrorCodeAuthenticationFailed, err.Error()
	}
	if apierrors.IsForbidden(err) {
		return http.StatusForbidden, kubebindv1alpha2.ErrorCodeAuthorizationFailed, err.Error()
	}
	if apierrors.IsBadRequest(err) {
		return http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, err.Error()
	}
	// Default to internal server error
	return http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, err.Error()
}

type handler struct {
	oidcProvider   auth.OIDCProvider
	authHandler    auth.AuthHandlerInterface
	authMiddleware *auth.AuthMiddleware

	scope              kubebindv1alpha2.InformerScope
	oidcAuthorizeURL   string
	backendCallbackURL string
	providerPrettyName string
	testingAutoSelect  string
	schemaSource       string

	cookieEncryptionKey []byte
	cookieSigningKey    []byte

	client      *http.Client
	kubeManager *kubernetes.Manager
	oidcServer  *oidc.Server

	frontend string
}

func NewHandler(
	oidcProvider auth.OIDCProvider,
	oidcServer *oidc.Server,
	oidcAuthorizeURL, backendCallbackURL, providerPrettyName, testingAutoSelect string,
	cookieSigningKey, cookieEncryptionKey []byte,
	schemaSource string,
	scope kubebindv1alpha2.InformerScope,
	mgr *kubernetes.Manager,
	frontend string,
	tokenExpiry time.Duration,
	sessionStore session.Store,
) (*handler, error) {
	// Create JWT service for CLI authentication
	jwtService, err := auth.NewJWTService("kube-bind-backend")
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT service: %w", err)
	}

	// Create auth middleware for request authentication
	authMiddleware := auth.NewAuthMiddleware(jwtService, cookieSigningKey, cookieEncryptionKey, mgr, sessionStore)

	// Create auth handler with OIDC provider
	authHandler := auth.NewAuthHandler(oidcProvider, jwtService, cookieSigningKey, cookieEncryptionKey, sessionStore, tokenExpiry)

	return &handler{
		oidcProvider:        oidcProvider,
		authHandler:         authHandler,
		authMiddleware:      authMiddleware,
		oidcAuthorizeURL:    oidcAuthorizeURL,
		backendCallbackURL:  backendCallbackURL,
		providerPrettyName:  providerPrettyName,
		testingAutoSelect:   testingAutoSelect,
		schemaSource:        schemaSource,
		frontend:            frontend,
		scope:               scope,
		client:              http.DefaultClient,
		kubeManager:         mgr,
		cookieSigningKey:    cookieSigningKey,
		cookieEncryptionKey: cookieEncryptionKey,
		oidcServer:          oidcServer,
	}, nil
}

func (h *handler) AddRoutes(mux *mux.Router) error {
	// Public API routes (no authentication required)
	mux.HandleFunc("/api/healthz", h.handleHealthz).Methods(http.MethodGet)
	mux.HandleFunc("/api/bindable-resources", h.handleBindableResources).Methods(http.MethodGet)
	// Intentionally unauthenticated: serves static, deterministic deployment YAML
	// (konnector image tag is the only variable, derived from the server's own version).
	// No secrets or cluster-specific data are included.
	mux.HandleFunc("/api/konnector-manifests", h.handleKonnectorManifests).Methods(http.MethodGet)

	// Generic authentication routes (support both UI and CLI)
	mux.HandleFunc("/api/authorize", h.authHandler.HandleAuthorize).Methods(http.MethodGet, http.MethodPost)
	mux.HandleFunc("/api/callback", h.authHandler.HandleCallback).Methods(http.MethodGet)
	mux.HandleFunc("/api/logout", h.handleLogout).Methods(http.MethodPost)

	mux.HandleFunc("/api/exports", h.handleServiceExport).Methods(http.MethodGet)
	mux.HandleFunc("/exports", h.handleExportsRedirect).Methods(http.MethodGet) // This provides HTTP 302 redirects for backwards compatibility with older CLI versions and in general nicer UX.

	// Protected API routes (require authentication)
	apiRouter := mux.PathPrefix("/api").Subrouter()
	apiRouter.Use(h.authMiddleware.AuthenticateRequest)

	apiRouter.Handle("/templates", auth.RequireAuth(http.HandlerFunc(h.handleTemplates))).Methods(http.MethodGet)
	apiRouter.Handle("/collections", auth.RequireAuth(http.HandlerFunc(h.handleCollections))).Methods(http.MethodGet)
	apiRouter.Handle("/bind", auth.RequireAuth(http.HandlerFunc(h.handleBind))).Methods(http.MethodPost)
	apiRouter.Handle("/ping", auth.RequireAuth(http.HandlerFunc(h.handlePing))).Methods(http.MethodGet)
	apiRouter.Handle("/consumer-status", auth.RequireAuth(http.HandlerFunc(h.handleConsumerStatus))).Methods(http.MethodGet)
	apiRouter.Handle("/apply-binding", auth.RequireAuth(http.HandlerFunc(h.handleApplyBinding))).Methods(http.MethodPost)

	if h.oidcServer != nil {
		h.oidcServer.AddRoutes(mux)
	}

	switch {
	// Development mode: proxy to frontend dev server
	case strings.HasPrefix(h.frontend, "http://"):
		spaserver, err := spaserver.NewSPAReverseProxyServer(h.frontend)
		if err != nil {
			return err
		}
		mux.PathPrefix("/").Handler(spaserver)
	default:
		fs := web.GetFileSystem()
		mux.PathPrefix("/").Handler(spaserver.NewSPAFileServer(fs))
	}
	return nil
}

func (h *handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	prepareNoCache(w)
	w.WriteHeader(http.StatusOK)
}

func (h *handler) handlePing(w http.ResponseWriter, r *http.Request) {
	prepareNoCache(w)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("pong")) //nolint:errcheck
}

// handleKonnectorManifests returns the pre-rendered konnector YAML manifests
// that a consumer cluster needs to apply to deploy the konnector agent.
// The manifests are generated from the same Go structs used by the one-click
// apply flow (ensureKonnector) to avoid definition drift.
func (h *handler) handleKonnectorManifests(w http.ResponseWriter, r *http.Request) {
	prepareNoCache(w)

	konnectorVersion, err := bindversion.BinaryVersion(componentbaseversion.Get().GitVersion)
	if err != nil {
		konnectorVersion = "latest"
	}
	konnectorImage := fmt.Sprintf("ghcr.io/kbind-dev/konnector:%s", konnectorVersion)

	manifests := kuberesources.NewKonnectorManifests(konnectorImage, h.kubeManager.GetKonnectorHostAliases())

	// Serialize each object to YAML and join with document separators
	s := runtime.NewScheme()
	kuberesources.AddKonnectorSchemes(s)
	encoder := kjson.NewSerializerWithOptions(
		kjson.DefaultMetaFactory,
		s,
		s,
		kjson.SerializerOptions{Yaml: true, Pretty: true, Strict: false},
	)
	codec := serializer.NewCodecFactory(s).EncoderForVersion(encoder, nil)

	var buf strings.Builder
	objects := manifests.Objects()
	for i, obj := range objects {
		if i > 0 {
			buf.WriteString("---\n")
		}
		if err := codec.Encode(obj, &buf); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to serialize konnector manifests", err.Error())
			return
		}
	}

	w.Header().Set("Content-Type", "text/yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=konnector.yaml")
	w.Write([]byte(buf.String())) //nolint:errcheck
}

func (h *handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	prepareNoCache(w)

	// Get cluster ID from query parameter if present
	clusterID := r.URL.Query().Get("cluster_id")

	// Determine cookie name based on cluster ID
	cookieName := "kube-bind"
	if clusterID != "" {
		cookieName = "kube-bind-" + clusterID
	}

	// Create an expired cookie to clear the existing one
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		Domain:   "",
		Expires:  time.Unix(0, 0), // Set to Unix epoch (Jan 1, 1970) to expire immediately
		HttpOnly: true,
		Secure:   r.TLS != nil, // Set secure flag if HTTPS
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("logged out")) //nolint:errcheck
}

func (h *handler) handleExportsRedirect(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	redirectURL := "/api/exports"

	// Preserve query parameters
	if r.URL.RawQuery != "" {
		redirectURL += "?" + r.URL.RawQuery
	}

	logger.Info("redirecting CLI exports request", "from", r.URL.Path, "to", redirectURL)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (h *handler) handleServiceExport(w http.ResponseWriter, r *http.Request) {
	logger := klog.FromContext(r.Context()).WithValues("method", r.Method, "url", r.URL.String())
	params := client.GetQueryParams(r)

	oidcAuthorizeURL := params.WithParams(fmt.Sprintf("http://%s/api/authorize", r.Host))

	ver, err := bindversion.BinaryVersion(componentbaseversion.Get().GitVersion)
	if err != nil {
		logger.Error(err, "failed to parse version %q", componentbaseversion.Get().GitVersion)
		ver = "v0.0.0"
	}
	prettyName := h.providerPrettyName
	if prettyName == "" {
		prettyName = "backend"
	}
	provider := &kubebindv1alpha2.BindingProvider{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kubebindv1alpha2.GroupVersion,
			Kind:       "BindingProvider",
		},
		Version:            ver,
		ProviderPrettyName: prettyName,
		AuthenticationMethods: []kubebindv1alpha2.AuthenticationMethod{
			{
				Method: "OAuth2CodeGrant",
				OAuth2CodeGrant: &kubebindv1alpha2.OAuth2CodeGrant{
					AuthenticatedURL: oidcAuthorizeURL,
				},
			},
		},
	}

	bs, err := json.Marshal(provider)
	if err != nil {
		logger.Error(err, "failed to marshal provider")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to marshal provider information", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(bs) //nolint:errcheck
}

func (h *handler) handleTemplates(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	prepareNoCache(w)
	logger.Info("getting templates")

	authenticated := auth.GetAuthContext(r.Context()).IsValid
	if !authenticated {
		logger.Info("unauthenticated request to get templates")
		writeErrorResponse(w, http.StatusUnauthorized, kubebindv1alpha2.ErrorCodeAuthenticationFailed, "Authentication required", "Valid authentication credentials are required to access templates")
		return
	}

	params := client.GetQueryParams(r)

	templates, err := h.listTemplates(r.Context(), params.ClusterID)
	if err != nil {
		logger.Error(err, "failed to get template resources")
		statusCode, code, details := mapErrorToCode(err)
		writeErrorResponse(w, statusCode, code, "Failed to retrieve templates", details)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// For UI, return direct result as before
	if err := json.NewEncoder(w).Encode(templates); err != nil {
		logger.Error(err, "failed to encode JSON response")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to encode response", err.Error())
	}
}

func (h *handler) handleCollections(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	prepareNoCache(w)
	logger.Info("getting collections")

	params := client.GetQueryParams(r)

	collections, err := h.listCollections(r.Context(), params.ClusterID)
	if err != nil {
		logger.Error(err, "failed to get collection resources")
		statusCode, code, details := mapErrorToCode(err)
		writeErrorResponse(w, statusCode, code, "Failed to retrieve collections", details)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// For UI, return direct result as before
	if err := json.NewEncoder(w).Encode(collections); err != nil {
		logger.Error(err, "failed to encode JSON response")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to encode response", err.Error())
	}
}

func (h *handler) handleBind(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	params := client.GetQueryParams(r)

	prepareNoCache(w)

	// Get auth context (already verified by middleware)
	authCtx := auth.GetAuthContext(r.Context())
	state := authCtx.SessionState

	// Parse JSON request body
	const maxBodySize = 1 << 20 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var bindRequest kubebindv1alpha2.BindableResourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&bindRequest); err != nil {
		logger.Error(err, "failed to parse JSON request body")
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeErrorResponse(w, http.StatusRequestEntityTooLarge, kubebindv1alpha2.ErrorCodeBadRequest, "Request body too large", "The request body exceeds the maximum allowed size")
		} else {
			writeErrorResponse(w, http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, "Invalid JSON request body", err.Error())
		}
		return
	}

	// Identity is always required. CLI provides the cluster identity (kube-system UID),
	// and the UI sends the well-known "ui-identity" value.
	identity := bindRequest.Spec.ClusterIdentity.Identity
	if identity == "" {
		writeErrorResponse(w, http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, "Missing cluster identity", "spec.clusterIdentity.identity is required")
		return
	}

	// Resolve the UI sentinel to a real identity derived from the authenticated session.
	isUIFlow := identity == auth.UIIdentity
	if isUIFlow {
		identity = state.Token.Issuer + "/" + state.Token.Subject
		logger.Info("Resolved ui-identity from session", "identity", identity)
	}

	consumerID := params.ConsumerID
	if consumerID == "" {
		consumerID = identity
	}

	clusterPrettyName := bindRequest.Spec.ClusterIdentity.PrettyName
	if clusterPrettyName == "" {
		clusterPrettyName = bindRequest.Name
	}

	handleResult, err := h.kubeManager.HandleResources(r.Context(), state.Token.Subject, consumerID, params.ClusterID, clusterPrettyName)
	if err != nil {
		logger.Error(err, "failed to handle resources")
		statusCode, code, details := mapErrorToCode(err)
		writeErrorResponse(w, statusCode, code, "Failed to handle cluster resources", details)
		return
	}

	// Module consist of many resources and permissionClaims. Read it and translate to
	template, err := h.kubeManager.GetTemplates(r.Context(), params.ClusterID, bindRequest.Spec.TemplateRef.Name)
	if err != nil {
		logger.Error(err, "failed to get template", "template", bindRequest.Spec.TemplateRef.Name, "cluster", params.ClusterID)
		statusCode, code, details := mapErrorToCode(err)
		writeErrorResponse(w, statusCode, code, "Failed to retrieve template", details)
		return
	}

	request := kubebindv1alpha2.APIServiceExportRequestResponse{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kubebindv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIServiceExportRequest",
		},
		ObjectMeta: kubebindv1alpha2.NameObjectMeta{
			Name: bindRequest.Name,
		},
		Spec: kubebindv1alpha2.APIServiceExportRequestSpec{
			Resources:          template.Spec.Resources,
			PermissionClaims:   template.Spec.PermissionClaims,
			Namespaces:         template.Spec.Namespaces,
			SchemaUpdatePolicy: template.Spec.SchemaUpdatePolicy,
		},
	}

	// For UI-only flow, create the APIServiceExportRequest on the provider cluster
	// and wait for reconciliation. In CLI flow the konnector handles this instead.
	var exportRequestName string
	if isUIFlow {
		exportRequest, err := h.kubeManager.CreateAPIServiceExportRequest(
			r.Context(),
			params.ClusterID,
			handleResult.Namespace,
			bindRequest.Name,
			request.Spec,
		)
		if err != nil {
			logger.Error(err, "failed to create APIServiceExportRequest")
			statusCode, code, details := mapErrorToCode(err)
			writeErrorResponse(w, statusCode, code, "Failed to create API service export request", details)
			return
		}
		exportRequestName = exportRequest.Name
	}

	// callback response
	requestBytes, err := json.Marshal(&request)
	if err != nil {
		logger.Error(err, "failed to marshal request")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to marshal request", err.Error())
		return
	}

	response := kubebindv1alpha2.BindingResourceResponse{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kubebindv1alpha2.SchemeGroupVersion.String(),
			Kind:       "BindingResponse",
		},
		Authentication: kubebindv1alpha2.BindingResponseAuthentication{
			OAuth2CodeGrant: &kubebindv1alpha2.BindingResponseAuthenticationOAuth2CodeGrant{
				SessionID: state.SessionID,
				ID:        state.Token.Issuer + "/" + state.Token.Subject,
			},
		},
		Kubeconfig:        handleResult.Kubeconfig,
		Requests:          []runtime.RawExtension{{Raw: requestBytes}},
		ProviderNamespace: handleResult.Namespace,
		BindingName:       exportRequestName,
	}

	payload, err := json.Marshal(&response)
	if err != nil {
		logger.Error(err, "failed to marshal binding response")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to marshal binding response", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(payload) //nolint:errcheck
}

// handleConsumerStatus returns whether the authenticated user already has a consumer
// namespace with existing APIServiceExports on the provider.
func (h *handler) handleConsumerStatus(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	params := client.GetQueryParams(r)
	prepareNoCache(w)

	authCtx := auth.GetAuthContext(r.Context())
	state := authCtx.SessionState
	identity := state.Token.Issuer + "/" + state.Token.Subject

	status, err := h.kubeManager.GetConsumerStatus(r.Context(), identity, params.ClusterID)
	if err != nil {
		logger.Error(err, "failed to get consumer status")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to get consumer status", err.Error())
		return
	}

	payload, err := json.Marshal(status)
	if err != nil {
		logger.Error(err, "failed to marshal consumer status")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to marshal consumer status", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(payload) //nolint:errcheck
}

// applyBindingRequest is the JSON body for the apply-binding endpoint.
type applyBindingRequest struct {
	// ConsumerKubeconfig is the base64-encoded kubeconfig for the consumer cluster.
	ConsumerKubeconfig string `json:"consumerKubeconfig"`
	// BindingName is the name for the binding (used for secret and bundle naming).
	BindingName string `json:"bindingName"`
	// HostAliases is an optional list of host alias entries for konnector pods,
	// in the format "IP:hostname1,hostname2". Overrides the server-configured defaults.
	HostAliases []string `json:"hostAliases,omitempty"`
}

// handleApplyBinding receives a consumer kubeconfig and applies the konnector + binding
// bundle to the consumer cluster.
func (h *handler) handleApplyBinding(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	params := client.GetQueryParams(r)
	prepareNoCache(w)

	authCtx := auth.GetAuthContext(r.Context())
	state := authCtx.SessionState
	identity := state.Token.Issuer + "/" + state.Token.Subject

	// Parse request body
	const maxBodySize = 1 << 20 // 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req applyBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, "Invalid request body", err.Error())
		return
	}

	if req.ConsumerKubeconfig == "" {
		writeErrorResponse(w, http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, "Missing consumer kubeconfig", "consumerKubeconfig is required")
		return
	}
	if req.BindingName == "" {
		writeErrorResponse(w, http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, "Missing binding name", "bindingName is required")
		return
	}

	// Decode base64 consumer kubeconfig
	consumerKubeconfigData, err := base64.StdEncoding.DecodeString(req.ConsumerKubeconfig)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, kubebindv1alpha2.ErrorCodeBadRequest, "Invalid consumer kubeconfig encoding", "consumerKubeconfig must be base64 encoded")
		return
	}

	// Get the provider kubeconfig for this user's namespace
	handleResult, err := h.kubeManager.HandleResources(r.Context(), state.Token.Subject, identity, params.ClusterID, req.BindingName)
	if err != nil {
		logger.Error(err, "failed to handle resources for apply-binding")
		statusCode, code, details := mapErrorToCode(err)
		writeErrorResponse(w, statusCode, code, "Failed to prepare provider resources", details)
		return
	}

	// Resolve konnector image
	konnectorVersion, err := bindversion.BinaryVersion(componentbaseversion.Get().GitVersion)
	if err != nil {
		konnectorVersion = "latest"
	}
	konnectorImage := fmt.Sprintf("ghcr.io/kbind-dev/konnector:%s", konnectorVersion)

	// Parse optional host alias overrides from request
	var overrideHostAliases []corev1.HostAlias
	for _, entry := range req.HostAliases {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			overrideHostAliases = append(overrideHostAliases, corev1.HostAlias{
				IP:        parts[0],
				Hostnames: strings.Split(parts[1], ","),
			})
		}
	}

	// Apply to consumer cluster
	result, err := h.kubeManager.ApplyToConsumer(
		r.Context(),
		consumerKubeconfigData,
		handleResult.Kubeconfig,
		req.BindingName,
		konnectorImage,
		overrideHostAliases,
	)
	if err != nil {
		logger.Error(err, "failed to apply binding to consumer cluster")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to apply binding to consumer cluster", err.Error())
		return
	}

	payload, err := json.Marshal(result)
	if err != nil {
		logger.Error(err, "failed to marshal apply result")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to marshal result", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(payload) //nolint:errcheck
}

// if they are part of a Collection or not.
func (h *handler) listTemplates(ctx context.Context, cluster string) (*kubebindv1alpha2.APIServiceExportTemplateList, error) {
	templates, err := h.kubeManager.ListTemplates(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}

	return templates, nil
}

// listCollections fetches the list of Collections from the backend cluster.
func (h *handler) listCollections(ctx context.Context, cluster string) (*kubebindv1alpha2.CollectionList, error) {
	collections, err := h.kubeManager.ListCollections(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}

	return collections, nil
}

func (h *handler) handleBindableResources(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)

	bs, err := json.Marshal(&kubebindv1alpha2.ClaimableAPIs)
	if err != nil {
		logger.Error(err, "failed to marshal resources")
		writeErrorResponse(w, http.StatusInternalServerError, kubebindv1alpha2.ErrorCodeInternalError, "Failed to marshal bindable resources", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(bs) //nolint:errcheck
}

// prepareNoCache prepares headers for preventing browser caching.
func prepareNoCache(w http.ResponseWriter) {
	// Set NoCache headers
	for k, v := range noCacheHeaders {
		w.Header().Set(k, v)
	}
}

func getLogger(r *http.Request) klog.Logger {
	return klog.FromContext(r.Context()).WithValues("method", r.Method, "url", r.URL.String())
}
