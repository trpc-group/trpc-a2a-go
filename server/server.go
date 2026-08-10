// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package server provides the A2A server implementation.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/telemetry"
	"trpc.group/trpc-go/trpc-a2a-go/v2/telemetry/metrics"
)

// A2AServer implements the HTTP server for the A2A protocol.
// It handles Agent Card requests and routes JSON-RPC and HTTP+JSON operations
// to the TaskManager.
type A2AServer struct {
	agentCard        AgentCard               // Default (single-agent) card; optional when multi-tenant.
	agentCardSet     bool                    // Whether a default card was provided (WithAgentCard).
	taskManager      taskmanager.TaskManager // Handles task logic.
	httpServer       *http.Server            // Underlying HTTP server.
	httpServerMu     sync.Mutex              // Guards httpServer: Start writes it, Stop reads it.
	corsEnabled      bool                    // Flag to enable/disable CORS headers.
	v1JSONRPCEnabled bool                    // Whether the v1 JSON-RPC binding is mounted.
	jsonRPCEndpoint  string                  // Path for the JSON-RPC endpoint.
	httpJSONBasePath string                  // Base path for HTTP+JSON/REST endpoints.
	httpJSONEnabled  bool                    // Whether the HTTP+JSON binding is mounted.
	httpJSONMaxBody  int64                   // Maximum HTTP+JSON request body size; <= 0 disables the limit.
	agentCardPath    string                  // Path for the agent card endpoint.
	oldAgentCardPath string                  // Path for the old agent card endpoint.
	// tenantCards is the static tenant -> AgentCard registry (WithTenantCards).
	// tenantCardProvider resolves a tenant's card dynamically (WithTenantCardProvider).
	// When either is set the server is multi-tenant: it routes by params.Tenant
	// (carried in the request body) and serves per-tenant cards, instead of the
	// legacy URL-path / placeholder-card mechanism.
	tenantCards        map[string]AgentCard
	tenantCardProvider func(ctx context.Context, tenant string) (AgentCard, error)
	readTimeout        time.Duration // HTTP server read timeout.
	writeTimeout       time.Duration // HTTP server write timeout.
	idleTimeout        time.Duration // HTTP server idle timeout.

	// compatHandler, when set, handles JSON-RPC requests whose method name is
	// not a v1.0 method (e.g. the legacy slash-delimited names). It is invoked
	// on the same endpoint and INSIDE the authentication middleware chain, so
	// the legacy protocol path is authenticated exactly like the v1 path.
	compatHandler http.Handler

	// Authentication related fields
	middleWare        []Middleware // Authentication middlewares.
	pushJWKSHandler   http.Handler // Publishes keys for verifying signed push notifications.
	jwksEnabled       bool         // Flag to enable/disable JWKS endpoint.
	jwksExplicitlySet bool         // WithJWKSEndpoint was called: an explicit decision wins over discovery.
	pushEnabled       bool         // Push posture derived from the TaskManager.
	jwksEndpoint      string       // Path for the JWKS endpoint.

	// Extended card related fields
	authenticatedCardHandler func(ctx context.Context, baseCard AgentCard) (AgentCard, error) // Dynamic card modifier function.

	// Telemetry fields
	firstTokenPolicy       telemetry.FirstTokenPolicy
	telemetryMeterProvider metric.MeterProvider
	telemetryOptions       []metrics.Option
	telemetryMetrics       *metrics.Instruments
	telemetryShutdown      func(context.Context) error
	telemetryOwnsProvider  bool
}

// NewA2AServer creates a new A2AServer instance with the given task manager.
//
// The agent card is configured via options rather than a positional argument:
// use WithAgentCard for a single-agent server, or WithTenantCard /
// WithTenantCardProvider for a multi-tenant server (one process hosting several
// agents, distinguished by the v1.0 tenant field). At least one of these must be
// provided. WithAgentCard is also accepted alongside the tenant options, where it
// becomes the default ("directory") card served when no "?tenant=" is given.
func NewA2AServer(taskManager taskmanager.TaskManager, opts ...Option) (*A2AServer, error) {
	if taskManager == nil {
		return nil, errors.New("NewA2AServer requires a non-nil taskManager")
	}
	server := &A2AServer{
		taskManager:      taskManager,
		corsEnabled:      true, // Enable CORS by default for easier development.
		v1JSONRPCEnabled: true,
		jsonRPCEndpoint:  protocol.DefaultJSONRPCPath,
		httpJSONBasePath: "",
		httpJSONMaxBody:  defaultHTTPJSONMaxBodyBytes,
		agentCardPath:    protocol.AgentCardPath,
		oldAgentCardPath: protocol.OldAgentCardPath,
		readTimeout:      defaultReadTimeout,
		writeTimeout:     defaultWriteTimeout,
		idleTimeout:      defaultIdleTimeout,
		jwksEnabled:      false,
		jwksEndpoint:     protocol.JWKSPath,
	}

	// Apply options (WithAgentCard, path options, middleware, …).
	for _, opt := range opts {
		opt(server)
	}

	// Require at least one card source so discovery and routing have something to serve.
	if !server.agentCardSet && len(server.tenantCards) == 0 && server.tenantCardProvider == nil {
		return nil, errors.New(
			"NewA2AServer requires an agent card: use WithAgentCard, WithTenantCard, or WithTenantCardProvider")
	}
	if !server.v1JSONRPCEnabled && server.compatHandler == nil && !server.httpJSONEnabled {
		return nil, errors.New(
			"NewA2AServer requires an enabled protocol binding: enable v1 JSON-RPC, HTTP+JSON, or compat/v0")
	}

	if err := server.resolvePushPosture(taskManager); err != nil {
		return nil, err
	}

	return server, nil
}

// resolvePushPosture derives the server's push-notification posture from the
// TaskManager and the configured options: whether push is enabled and whether
// to publish JWKS (a handler is configured and push is enabled, unless
// WithJWKSEndpoint decided otherwise). It returns an error when a JWKS endpoint
// was requested without a handler.
func (s *A2AServer) resolvePushPosture(taskManager taskmanager.TaskManager) error {
	// The server depends only on the manager's semantic capability; a manager may
	// deliver through HTTP, a queue, or a durable outbox.
	s.pushEnabled = taskManager.SupportsPushNotifications()
	if err := s.validateStaticPushCapabilities(); err != nil {
		return err
	}
	// A configured JWKS handler publishes verification keys, but it is NOT by
	// itself a delivery capability: publishing keys also requires
	// push to be enabled, so the card never advertises push it cannot honor. An
	// explicit WithJWKSEndpoint decision still wins.
	if s.pushJWKSHandler != nil && s.pushEnabled && !s.jwksExplicitlySet {
		s.jwksEnabled = true
	}

	// Log the resolved delivery and key-publication posture. The server cannot
	// infer whether a custom Sender signs its deliveries.
	switch {
	case s.pushEnabled && s.pushJWKSHandler != nil && s.jwksEnabled:
		log.Infof("push: enabled, JWKS at %s", s.jwksEndpoint)
	case s.pushEnabled && s.pushJWKSHandler != nil:
		log.Infof("push: enabled, JWKS publication disabled by option")
	case s.pushEnabled:
		log.Infof("push: enabled, no JWKS handler configured")
	default:
		log.Debugf("push: disabled")
	}

	// An enabled JWKS endpoint needs a handler to serve it. Fail at construction
	// instead of silently leaving the advertised endpoint unavailable.
	if s.jwksEnabled && s.pushJWKSHandler == nil {
		return fmt.Errorf("JWKS endpoint enabled without a handler: " +
			"pass server.WithPushNotificationJWKSHandler (e.g. sender.JWKSHandler()), " +
			"or disable publication with server.WithJWKSEndpoint(false, \"\")")
	}
	return nil
}

func (s *A2AServer) validateStaticPushCapabilities() error {
	validate := func(name string, card AgentCard) error {
		declared := card.Capabilities.PushNotifications
		// A card may deliberately disable push for one tenant even when the shared
		// manager supports it. The unsafe mismatch is advertising true when the
		// manager cannot honor the operations.
		if declared != nil && *declared && !s.pushEnabled {
			return fmt.Errorf("agent card %s declares pushNotifications=true but the task manager does not support push", name)
		}
		return nil
	}
	if s.agentCardSet {
		if err := validate("(default)", s.agentCard); err != nil {
			return err
		}
	}
	for tenant, c := range s.tenantCards {
		if err := validate(tenant, c); err != nil {
			return err
		}
	}
	return nil
}

// Start begins listening for HTTP requests on the specified network address.
// It blocks until the server is stopped via Stop() or an error occurs.
func (s *A2AServer) Start(address string) error {
	if err := s.InitTelemetry(context.Background()); err != nil {
		return fmt.Errorf("initialize telemetry: %w", err)
	}
	httpServer := &http.Server{
		Addr:         address,
		Handler:      s.Handler(),
		ReadTimeout:  s.readTimeout,
		WriteTimeout: s.writeTimeout,
		IdleTimeout:  s.idleTimeout,
	}
	// Publish under the lock before ListenAndServe blocks, so a concurrent
	// Stop() always observes a fully constructed server (Start typically runs
	// in its own goroutine).
	s.httpServerMu.Lock()
	s.httpServer = httpServer
	s.httpServerMu.Unlock()

	log.Infof("Starting A2A server listening on %s...", address)
	// ListenAndServe blocks. It returns http.ErrServerClosed on graceful shutdown.
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		if shutdownErr := s.shutdownTelemetry(context.Background()); shutdownErr != nil {
			return errors.Join(
				fmt.Errorf("http server ListenAndServe error: %w", err),
				fmt.Errorf("telemetry shutdown failed: %w", shutdownErr),
			)
		}
		return fmt.Errorf("http server ListenAndServe error: %w", err)
	}
	log.Info("A2A server stopped.")
	return nil
}

// Stop gracefully shuts down the running HTTP server.
// It waits for active connections to finish within the provided context's deadline.
func (s *A2AServer) Stop(ctx context.Context) error {
	s.httpServerMu.Lock()
	httpServer := s.httpServer
	s.httpServerMu.Unlock()
	if httpServer == nil {
		return errors.New("A2A server not running")
	}
	log.Info("Attempting graceful shutdown of A2A server...")
	var shutdownErrs []error
	if err := httpServer.Shutdown(ctx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("http server shutdown failed: %w", err))
	}
	// Release task manager resources (e.g. stop the in-memory cleanup goroutine or
	// close the Redis client) once in-flight requests have drained. Done via the
	// optional io.Closer to avoid widening the TaskManager interface.
	if closer, ok := s.taskManager.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("task manager close failed: %w", err))
		}
	}
	if err := s.shutdownTelemetry(ctx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("telemetry shutdown failed: %w", err))
	}
	if len(shutdownErrs) > 0 {
		return errors.Join(shutdownErrs...)
	}
	log.Info("A2A server shutdown complete.")
	return nil
}

type telemetryShutdowner interface {
	Shutdown(context.Context) error
}

func (s *A2AServer) initTelemetry(ctx context.Context) error {
	if s.telemetryMetrics != nil &&
		(s.telemetryMeterProvider == nil || s.telemetryMetrics.MeterProvider == s.telemetryMeterProvider) {
		return nil
	}
	if s.telemetryMeterProvider == nil && len(s.telemetryOptions) > 0 {
		mp, err := metrics.NewMeterProvider(ctx, s.telemetryOptions...)
		if err != nil {
			return fmt.Errorf("create meter provider: %w", err)
		}
		s.telemetryMeterProvider = mp
		s.telemetryShutdown = mp.Shutdown
		s.telemetryOwnsProvider = true
	}
	if s.telemetryMeterProvider == nil {
		return nil
	}
	if s.telemetryOwnsProvider && s.telemetryShutdown == nil {
		if shutdowner, ok := s.telemetryMeterProvider.(telemetryShutdowner); ok {
			s.telemetryShutdown = shutdowner.Shutdown
		}
	}
	instruments, err := metrics.NewInstruments(s.telemetryMeterProvider)
	if err != nil {
		return fmt.Errorf("create metric instruments: %w", err)
	}
	s.telemetryMetrics = instruments
	return nil
}

func (s *A2AServer) shutdownTelemetry(ctx context.Context) error {
	s.telemetryMetrics = nil
	if !s.telemetryOwnsProvider || s.telemetryShutdown == nil {
		return nil
	}
	err := s.telemetryShutdown(ctx)
	s.telemetryMeterProvider = nil
	s.telemetryShutdown = nil
	s.telemetryOwnsProvider = false
	return err
}

// Handler returns an http.Handler for the server.
// This can be used to integrate the A2A server into existing HTTP servers.
func (s *A2AServer) Handler() http.Handler {
	router := http.NewServeMux()

	cardHandler := s.agentCardHandler()
	router.Handle(s.agentCardPath, cardHandler)
	router.Handle(s.oldAgentCardPath, cardHandler)
	if jwks := s.jwksHandler(); jwks != nil {
		router.Handle(s.jwksEndpoint, jwks)
	}

	jsonRPCHandler := s.jsonRPCHandler()
	httpJSONHandler := s.httpJSONHandler()
	if httpJSONHandler == nil {
		router.Handle(s.jsonRPCEndpoint, s.withMiddleware(jsonRPCHandler))
		return rawPathGuard(router)
	}

	httpJSONPattern := httpJSONServeMuxPattern(s.httpJSONBasePath)
	if jsonRPCHandler == nil {
		router.Handle(httpJSONPattern, s.withMiddleware(httpJSONHandler))
		return rawPathGuard(router)
	}

	if strings.HasPrefix(s.jsonRPCEndpoint, httpJSONPattern) {
		// The HTTP+JSON pattern covers the JSON-RPC endpoint. Register one
		// dispatcher at the wider pattern so a tenant whose name matches the
		// JSON-RPC path prefix is still routed as HTTP+JSON. Only the exact
		// configured endpoint is JSON-RPC.
		escapedJSONRPCEndpoint := (&url.URL{Path: s.jsonRPCEndpoint}).EscapedPath()
		combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() == escapedJSONRPCEndpoint {
				jsonRPCHandler.ServeHTTP(w, r)
				return
			}
			httpJSONHandler.ServeHTTP(w, r)
		})
		router.Handle(httpJSONPattern, s.withMiddleware(combined))
		return rawPathGuard(router)
	}

	router.Handle(s.jsonRPCEndpoint, s.withMiddleware(jsonRPCHandler))
	router.Handle(httpJSONPattern, s.withMiddleware(httpJSONHandler))
	return rawPathGuard(router)
}

// rawPathGuard rejects path forms that http.ServeMux would otherwise clean and
// redirect before the A2A router sees them. A redirect can remove a tenant
// segment or turn a task resource into a collection operation.
func rawPathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(r.URL.EscapedPath(), "/")
		for i, segment := range segments {
			if segment == "" {
				if i > 0 && i < len(segments)-1 {
					http.Error(w, "invalid request path", http.StatusBadRequest)
					return
				}
				continue
			}
			decoded, err := url.PathUnescape(segment)
			if err != nil || decoded == "." || decoded == ".." {
				http.Error(w, "invalid request path", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// agentCardHandler serves the public agent card (not middleware-wrapped).
func (s *A2AServer) agentCardHandler() http.Handler {
	return http.HandlerFunc(s.handleAgentCard)
}

// jwksHandler returns the JWKS publication handler when enabled, else nil.
func (s *A2AServer) jwksHandler() http.Handler {
	if !s.jwksEnabled || s.pushJWKSHandler == nil {
		return nil
	}
	return s.pushJWKSHandler
}

// jsonRPCHandler returns the JSON-RPC binding handler with compat dispatch
// included. Middleware is applied by Handler after transport routing is built.
func (s *A2AServer) jsonRPCHandler() http.Handler {
	if !s.v1JSONRPCEnabled {
		return s.compatHandler
	}
	h := http.Handler(http.HandlerFunc(s.handleJSONRPC))
	if s.compatHandler != nil {
		// Compat runs inside the same handler so auth middleware covers both
		// protocol generations when Handler() wraps the result.
		h = s.dispatchByProtocol(h, s.compatHandler)
	}
	return h
}

// httpJSONHandler returns the HTTP+JSON binding handler when enabled, else nil.
// Middleware is applied by Handler after transport routing is built.
func (s *A2AServer) httpJSONHandler() http.Handler {
	if !s.httpJSONEnabled {
		return nil
	}
	return http.HandlerFunc(s.handleHTTPJSON)
}

func (s *A2AServer) withMiddleware(h http.Handler) http.Handler {
	if len(s.middleWare) == 0 {
		return h
	}
	return MiddlewareChain(s.middleWare).Wrap(h)
}

// dispatchByProtocol returns a handler that routes a JSON-RPC POST to the
// legacy handler when its "method" is a legacy (slash-delimited) name and no
// protocol version was supplied. An explicit version must be validated by the
// v1 handler and cannot bypass negotiation merely by using a legacy method.
// Non-POST requests and unreadable bodies fall through to the v1 handler.
func (s *A2AServer) dispatchByProtocol(v1Handler, legacyHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			v1Handler.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if closeErr := r.Body.Close(); closeErr != nil {
			log.Errorf("Failed to close request body during protocol dispatch: %v", closeErr)
		}
		if err != nil {
			s.writeJSONRPCError(w, nil, jsonrpc.ErrParseError(fmt.Sprintf("failed to read request body: %v", err)))
			return
		}
		// Restore the body for the downstream handler.
		r.Body = io.NopCloser(bytes.NewReader(body))

		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &probe) == nil &&
			strings.Contains(probe.Method, "/") && requestedA2AVersion(r) == "" {
			legacyHandler.ServeHTTP(w, r)
			return
		}
		v1Handler.ServeHTTP(w, r)
	})
}

// handleAgentCard serves the agent's metadata card as JSON.
// Corresponds to GET /.well-known/agent-card.json in A2A Spec.
func (s *A2AServer) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	if s.corsEnabled {
		setCORSHeaders(w)
	}
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	// Multi-tenant: serve the card for "?tenant=<tenant>"; empty -> default card.
	card, ok := s.resolveAgentCard(r.Context(), r.URL.Query().Get("tenant"))
	if !ok {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", protocol.MediaTypeJSON+"; charset=utf-8")
	if err := json.NewEncoder(w).Encode(card); err != nil {
		log.Errorf("Failed to encode agent card: %v", err)
		// Avoid writing JSON-RPC error here; it's a standard HTTP endpoint.
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

// finalizePushCapability fills a served card's push capability from the server's
// resolved push posture. It runs on the by-value copy every card path returns,
// so stored and user-owned cards are never mutated. Rules: only a nil (unset)
// capability is filled, an explicit user value is never overridden, and signed
// cards are immutable (filling a field would invalidate the JWS) so they pass
// through untouched.
func (s *A2AServer) finalizePushCapability(card AgentCard) AgentCard {
	if !s.pushEnabled || card.Capabilities.PushNotifications != nil || len(card.Signatures) > 0 {
		return card
	}
	enabled := true
	card.Capabilities.PushNotifications = &enabled
	return card
}

// resolveAgentCard returns the AgentCard for the given tenant (from "?tenant=").
// The bool reports whether a card was found: a successful lookup returns
// (card, true), while a missing default or unknown tenant returns (AgentCard{},
// false). An empty tenant selects the default card. On a multi-tenant server
// (WithTenantCard / WithTenantCardProvider), an unknown tenant is not found.
func (s *A2AServer) resolveAgentCard(ctx context.Context, tenant string) (AgentCard, bool) {
	if tenant == "" {
		// No default card configured (pure multi-tenant): nothing to serve without a tenant.
		if !s.agentCardSet {
			return AgentCard{}, false
		}
		return s.finalizePushCapability(s.agentCard), true
	}
	if c, ok := s.tenantCards[tenant]; ok {
		return s.finalizePushCapability(c), true
	}
	if s.tenantCardProvider != nil {
		c, err := s.tenantCardProvider(ctx, tenant)
		if err != nil {
			return AgentCard{}, false
		}
		return s.finalizePushCapability(c), true
	}
	if len(s.tenantCards) > 0 {
		// Multi-tenant server, but no such tenant.
		return AgentCard{}, false
	}
	// Single-agent server: ignore an unexpected tenant param, serve the default.
	if !s.agentCardSet {
		return AgentCard{}, false
	}
	return s.finalizePushCapability(s.agentCard), true
}

func (s *A2AServer) pushAvailableForTenant(ctx context.Context, tenant string) bool {
	if !s.pushEnabled {
		return false
	}
	card, ok := s.resolveAgentCard(ctx, tenant)
	return ok && card.Capabilities.PushNotifications != nil && *card.Capabilities.PushNotifications
}

// handleJSONRPC is the main handler for all JSON-RPC 2.0 requests.
// Routes methods like tasks/send, tasks/get, etc., as defined in A2A Spec.
func (s *A2AServer) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	// --- CORS Handling ---
	if s.corsEnabled {
		setCORSHeaders(w)
		// Handle browser preflight requests.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Validate request basics
	if !s.validateJSONRPCRequest(w, r) {
		return
	}

	// Read and parse JSON-RPC request
	request, err := s.parseJSONRPCRequest(w, r.Body)
	if err != nil {
		return
	}

	// Route to appropriate handler based on method
	s.routeJSONRPCMethod(r.Context(), w, request)
}

// validateJSONRPCRequest validates basic HTTP requirements for JSON-RPC.
// Returns true if valid, writes error and returns false if invalid.
func (s *A2AServer) validateJSONRPCRequest(w http.ResponseWriter, r *http.Request) bool {
	// Check HTTP method
	if r.Method != http.MethodPost {
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrInvalidRequest(fmt.Sprintf("HTTP method %s not allowed, use POST", r.Method)))
		return false
	}

	// Check Content-Type using mime parsing
	contentType := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != protocol.MediaTypeJSON {
		log.Warnf("Rejecting request due to invalid Content-Type: '%s' (Parse Err: %v)", contentType, err)
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrInvalidRequest(
				fmt.Sprintf("Content-Type header must be application/json, got: %s", contentType)))
		return false
	}

	// Spec §9.2 routes the A2A service parameters through HTTP headers on this
	// binding too, so an unsupported version must be rejected here.
	if versionErr := validateA2AVersion(r); versionErr != nil {
		s.writeJSONRPCError(w, nil, jsonrpc.FromTaskManagerError(versionErr))
		return false
	}

	return true
}

// validateA2AVersion rejects a request that asks for a protocol version this
// build does not speak. Per spec §3.6.1 the version may travel as the
// A2A-Version header or as a request parameter. Per §3.6.2 an absent value is
// interpreted as 0.3, and patch versions do not participate in negotiation.
func validateA2AVersion(r *http.Request) *taskmanager.Error {
	requested := requestedA2AVersion(r)
	if requested == "" {
		requested = "0.3"
	}
	parts := strings.Split(requested, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return taskmanager.ErrVersionNotSupported(requested)
	}
	values := make([]uint64, len(parts))
	for i, part := range parts {
		if part == "" {
			return taskmanager.ErrVersionNotSupported(requested)
		}
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return taskmanager.ErrVersionNotSupported(requested)
		}
		values[i] = value
	}
	supported := strings.Split(protocol.ProtocolVersionV1, ".")
	if len(supported) != 2 ||
		strconv.FormatUint(values[0], 10) != supported[0] ||
		strconv.FormatUint(values[1], 10) != supported[1] {
		return taskmanager.ErrVersionNotSupported(requested)
	}
	return nil
}

func requestedA2AVersion(r *http.Request) string {
	requested := r.Header.Get("A2A-Version")
	if requested == "" {
		requested = r.URL.Query().Get("A2A-Version")
		if requested == "" {
			for name, values := range r.URL.Query() {
				if strings.EqualFold(name, "A2A-Version") && len(values) > 0 {
					requested = values[0]
					break
				}
			}
		}
	}
	return requested
}

// parseJSONRPCRequest reads the request body and parses it into a JSON-RPC request.
// Returns the request and nil if successful, or nil and error if parsing failed.
func (s *A2AServer) parseJSONRPCRequest(w http.ResponseWriter, body io.ReadCloser) (jsonrpc.Request, error) {
	var request jsonrpc.Request

	// Read the request body
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrParseError(fmt.Sprintf("failed to read request body: %v", err)))
		return request, err
	}

	// It's important to close the body, even though ReadAll consumes it
	defer body.Close()

	// Parse the JSON request
	if err := json.Unmarshal(bodyBytes, &request); err != nil {
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrParseError(fmt.Sprintf("failed to parse JSON request: %v", err)))
		return request, err
	}

	// Validate JSON-RPC version
	if request.JSONRPC != jsonrpc.Version {
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrInvalidRequest(fmt.Sprintf("jsonrpc field must be '%s'", jsonrpc.Version)))
		return request, fmt.Errorf("invalid JSON-RPC version")
	}

	return request, nil
}

// routeJSONRPCMethod routes the request to the appropriate handler based on the method.
func (s *A2AServer) routeJSONRPCMethod(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	log.Debugf("Received JSON-RPC request (ID: %v, Method: %s)", request.ID, request.Method)

	switch request.Method {

	case protocol.MethodMessageSend: // A2A Spec: message/send
		s.handleMessageSend(ctx, w, request)
	case protocol.MethodMessageStream: // A2A Spec: message/stream
		s.handleMessageStream(ctx, w, request)
	case protocol.MethodTasksPushNotificationConfigGet: // A2A Spec: tasks/pushNotification/config/get
		s.handleTasksPushNotificationGet(ctx, w, request)
	case protocol.MethodTasksPushNotificationConfigSet: // A2A Spec: tasks/pushNotification/config/set
		s.handleTasksPushNotificationSet(ctx, w, request)
	case protocol.MethodTasksGet: // A2A Spec: GetTask
		s.handleTasksGet(ctx, w, request)
	case protocol.MethodTasksList: // A2A Spec: ListTasks (v1.0)
		s.handleTasksList(ctx, w, request)
	case protocol.MethodTasksCancel: // A2A Spec: CancelTask
		s.handleTasksCancel(ctx, w, request)
	case protocol.MethodTasksPushNotificationConfigList: // A2A Spec: ListTaskPushNotificationConfigs (v1.0)
		s.handleTasksPushNotificationList(ctx, w, request)
	case protocol.MethodTasksPushNotificationConfigDelete: // A2A Spec: DeleteTaskPushNotificationConfig (v1.0)
		s.handleTasksPushNotificationDelete(ctx, w, request)
	case protocol.MethodTasksResubscribe: // A2A Spec: tasks/resubscribe
		s.handleTasksResubscribe(ctx, w, request)
	case protocol.MethodAgentAuthenticatedExtendedCard: // A2A Spec: agent/getAuthenticatedExtendedCard
		s.handleAgentGetAuthenticatedExtendedCard(ctx, w, request)
	default:
		log.Warnf("Method not found: %s (Request ID: %v)", request.Method, request.ID)
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrMethodNotFound(fmt.Sprintf("method '%s' not supported", request.Method)))
	}
}

// unmarshalParams is a helper function to unmarshal JSON-RPC params into the provided struct.
// It returns an error if unmarshalling fails, which is already formatted as a JSON-RPC error.
func (s *A2AServer) unmarshalParams(params json.RawMessage, v interface{}) *jsonrpc.Error {
	if err := json.Unmarshal(params, v); err != nil {
		return jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err))
	}
	return nil
}

// validateSendMessageParams checks the shared SendMessage / SendStreamingMessage
// request shape before either handler opens a task-manager round.
func (s *A2AServer) validateSendMessageParams(
	ctx context.Context, params *protocol.SendMessageParams,
) error {
	if params.Message.Role != protocol.MessageRoleUser {
		return taskmanager.ErrInvalidParams("message role must be ROLE_USER")
	}
	if len(params.Message.Parts) == 0 {
		return taskmanager.ErrInvalidParams("message with at least one part is required")
	}
	for _, part := range params.Message.Parts {
		if part == nil {
			return taskmanager.ErrInvalidParams("message parts must not contain null")
		}
	}
	if params.Configuration != nil && params.Configuration.PushConfig != nil &&
		!s.pushAvailableForTenant(ctx, params.Tenant) {
		return taskmanager.ErrPushNotificationNotSupported()
	}
	return nil
}

// handleTasksGet handles the tasks_get method.
func (s *A2AServer) handleTasksGet(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.TaskQueryParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	task, err := s.taskManager.OnGetTask(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnGetTask", params.ID)
		return
	}
	s.writeJSONRPCResponse(w, request.ID, task)
}

// handleTasksList handles the v1.0 ListTasks method.
func (s *A2AServer) handleTasksList(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.ListTasksParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	result, err := s.taskManager.OnListTasks(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnListTasks", params.ContextID)
		return
	}
	s.writeJSONRPCResponse(w, request.ID, result)
}

// handleTasksPushNotificationList handles the v1.0 ListTaskPushNotificationConfigs method.
func (s *A2AServer) handleTasksPushNotificationList(
	ctx context.Context, w http.ResponseWriter, request jsonrpc.Request,
) {
	var params protocol.ListTaskPushNotificationConfigsParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	if params.TaskID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	if !s.pushAvailableForTenant(ctx, params.Tenant) {
		s.writeJSONRPCError(w, request.ID, jsonrpc.FromTaskManagerError(taskmanager.ErrPushNotificationNotSupported()))
		return
	}
	result, err := s.taskManager.OnPushNotificationList(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnPushNotificationList", params.TaskID)
		return
	}
	s.writeJSONRPCResponse(w, request.ID, result)
}

// handleTasksPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig method.
func (s *A2AServer) handleTasksPushNotificationDelete(
	ctx context.Context, w http.ResponseWriter, request jsonrpc.Request,
) {
	var params protocol.DeleteTaskPushNotificationConfigParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	if params.TaskID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrInvalidParams("push notification config ID is required"))
		return
	}
	if !s.pushAvailableForTenant(ctx, params.Tenant) {
		s.writeJSONRPCError(w, request.ID, jsonrpc.FromTaskManagerError(taskmanager.ErrPushNotificationNotSupported()))
		return
	}
	if err := s.taskManager.OnPushNotificationDelete(ctx, params); err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnPushNotificationDelete", params.TaskID)
		return
	}
	// v1.0 DeleteTaskPushNotificationConfig returns an empty result on success.
	s.writeJSONRPCResponse(w, request.ID, struct{}{})
}

// handleTasksCancel handles the tasks_cancel method.
func (s *A2AServer) handleTasksCancel(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.TaskIDParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	task, err := s.taskManager.OnCancelTask(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnCancelTask", params.ID)
		return
	}
	s.writeJSONRPCResponse(w, request.ID, task)
}

// handleTaskManagerError maps a binding-neutral TaskManager error to JSON-RPC.
func (s *A2AServer) handleTaskManagerError(
	w http.ResponseWriter,
	id interface{},
	err error,
	operation string,
	taskID string,
) {
	var taskErr *taskmanager.Error
	if errors.As(err, &taskErr) {
		log.Errorf("Error calling %s for task %s: %v", operation, taskID, taskErr)
		s.writeJSONRPCError(w, id, jsonrpc.FromTaskManagerError(taskErr))
		return
	}
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		log.Errorf("Error calling %s for task %s: %v", operation, taskID, rpcErr)
		s.writeJSONRPCError(w, id, rpcErr)
		return
	}
	log.Errorf("Unexpected error calling %s for task %s: %v", operation, taskID, err)
	s.writeJSONRPCError(w, id, jsonrpc.ErrInternalError(fmt.Sprintf("%s failed: %v", operation, err)))
}

// writeJSONRPCResponse encodes and writes a successful JSON-RPC response.
func (s *A2AServer) writeJSONRPCResponse(w http.ResponseWriter, id interface{}, result interface{}) {
	response := jsonrpc.NewResponse(id, result)
	w.Header().Set("Content-Type", protocol.MediaTypeJSON+"; charset=utf-8")
	w.WriteHeader(http.StatusOK) // Success is always 200 OK for JSON-RPC itself.
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Log error, but can't change response if headers are already sent.
		log.Errorf("Failed to write JSON-RPC success response (ID: %v): %v", id, err)
	}
}

// writeJSONRPCError encodes and writes a JSON-RPC error response.
// It attempts to set an appropriate HTTP status code based on the JSON-RPC error code.
func (s *A2AServer) writeJSONRPCError(w http.ResponseWriter, id interface{}, err *jsonrpc.Error) {
	if err == nil {
		// Should not happen, but handle defensively.
		err = jsonrpc.ErrInternalError("writeJSONRPCError called with nil error")
		log.Errorf("Programming ERROR: writeJSONRPCError called with nil error (Request ID: %v)", id)
	}
	response := jsonrpc.NewErrorResponse(id, err)
	w.Header().Set("Content-Type", protocol.MediaTypeJSON+"; charset=utf-8")
	// Map JSON-RPC error codes to HTTP status codes where appropriate.
	httpStatus := http.StatusInternalServerError // Default for Internal errors.
	switch err.Code {
	case jsonrpc.CodeParseError:
		httpStatus = http.StatusBadRequest
	case jsonrpc.CodeInvalidRequest:
		httpStatus = http.StatusBadRequest
	case jsonrpc.CodeMethodNotFound:
		httpStatus = http.StatusNotFound
	case jsonrpc.CodeInvalidParams:
		httpStatus = http.StatusBadRequest
	case jsonrpc.CodeTaskNotFound:
		httpStatus = http.StatusNotFound
	case jsonrpc.CodeTaskNotCancelable,
		jsonrpc.CodePushNotificationNotSupported,
		jsonrpc.CodeUnsupportedOperation,
		jsonrpc.CodeContentTypeNotSupported,
		jsonrpc.CodeAuthenticatedExtendedCardNotConfigured,
		jsonrpc.CodeExtensionSupportRequired,
		jsonrpc.CodeVersionNotSupported:
		httpStatus = http.StatusBadRequest
		// ErrCodeInvalidAgentResponse and other internal errors keep the 500 default.
	}
	w.WriteHeader(httpStatus)
	if encodeErr := json.NewEncoder(w).Encode(response); encodeErr != nil {
		// Log error, but can't change response now.
		log.Errorf("Failed to write JSON-RPC error response (ID: %v, Code: %d): %v", id, err.Code, encodeErr)
	}
}

// setCORSHeaders adds permissive CORS headers for development/testing.
// WARNING: This is insecure for production. Configure origins explicitly.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*") // INSECURE
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, A2A-Version, A2A-Extensions")
	// Max-Age might be useful but not strictly necessary here.
}

func (s *A2AServer) handleTasksPushNotificationSet(
	ctx context.Context,
	w http.ResponseWriter,
	request jsonrpc.Request,
) {
	var params protocol.TaskPushNotificationConfig
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	if err := push.ValidateConfig(params); err != nil {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams(err.Error()))
		return
	}
	if !s.pushAvailableForTenant(ctx, params.Tenant) {
		s.writeJSONRPCError(w, request.ID, jsonrpc.FromTaskManagerError(taskmanager.ErrPushNotificationNotSupported()))
		return
	}
	result, err := s.taskManager.OnPushNotificationSet(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnPushNotificationSet", params.TaskID)
		return
	}
	s.writeJSONRPCResponse(w, request.ID, result)
}

func (s *A2AServer) handleTasksPushNotificationGet(
	ctx context.Context,
	w http.ResponseWriter,
	request jsonrpc.Request,
) {
	var params protocol.GetTaskPushNotificationConfigParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	if params.TaskID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrInvalidParams("push notification config ID is required"))
		return
	}
	if !s.pushAvailableForTenant(ctx, params.Tenant) {
		s.writeJSONRPCError(w, request.ID, jsonrpc.FromTaskManagerError(taskmanager.ErrPushNotificationNotSupported()))
		return
	}
	result, err := s.taskManager.OnPushNotificationGet(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnPushNotificationGet", params.TaskID)
		return
	}

	s.writeJSONRPCResponse(w, request.ID, result)
}

func (s *A2AServer) handleTasksResubscribe(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.TaskIDParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	// Validate required fields.
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}

	// Ensure client is accepting SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Error("Streaming is not supported by the underlying http responseWriter")
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInternalError("server does not support streaming"))
		return
	}

	// Get the event channel from the task manager.
	eventsChan, err := s.taskManager.OnResubscribe(ctx, params)
	if err != nil {
		s.handleTaskManagerError(w, request.ID, err, "OnResubscribe", params.ID)
		return
	}

	// Use the helper function to handle the SSE stream
	log.Debugf("SSE stream reopened for request ID: %v)", request.ID)
	handleSSEStream(ctx, s.corsEnabled, w, flusher, eventsChan, request.ID, nil, sse.FormatJSONRPCEventBatch)
}

// handleMessageSend handles the message_send method.
func (s *A2AServer) handleMessageSend(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	tracker := newMetricsTracker(protocol.MethodMessageSend, false, s.firstTokenPolicy, s.telemetryMetrics)
	defer tracker.record(ctx)

	var params protocol.SendMessageParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		tracker.setError(errTypeInvalidParams)
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	if err := s.validateSendMessageParams(ctx, &params); err != nil {
		tracker.setValidateSendMessageError(err)
		s.writeJSONRPCError(w, request.ID, jsonrpc.FromTaskManagerError(err))
		return
	}
	// Delegate to the task manager.
	message, err := s.taskManager.OnSendMessage(ctx, params)
	if err != nil {
		tracker.setError(errTypeMessageProcessingFailed)
		s.handleTaskManagerError(w, request.ID, err, "OnSendMessage", params.RPCID)
		return
	}
	tracker.observeNonStreamingResult(message)
	s.writeJSONRPCResponse(w, request.ID, message)
}

// handleMessageStream handles the message_stream method using Server-Sent Events (SSE).
func (s *A2AServer) handleMessageStream(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	tracker := newMetricsTracker(protocol.MethodMessageStream, true, s.firstTokenPolicy, s.telemetryMetrics)

	var params protocol.SendMessageParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		tracker.setError(errTypeInvalidParams)
		tracker.record(ctx)
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	if err := s.validateSendMessageParams(ctx, &params); err != nil {
		tracker.setValidateSendMessageError(err)
		tracker.record(ctx)
		s.writeJSONRPCError(w, request.ID, jsonrpc.FromTaskManagerError(err))
		return
	}

	// Check if client supports SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Error("Streaming is not supported by the underlying http responseWriter")
		tracker.setError(errTypeStreamingNotSupported)
		tracker.record(ctx)
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInternalError("server does not support streaming"))
		return
	}

	// Get the event channel from the task manager.
	eventsChan, err := s.taskManager.OnSendMessageStream(ctx, params)
	if err != nil {
		tracker.setError(errTypeSubscribeFailed)
		tracker.record(ctx)
		// Preserves a core *jsonrpc.Error (e.g. invalid params) and wraps the rest.
		s.handleTaskManagerError(w, request.ID, err, "OnSendMessageStream", params.RPCID)
		return
	}

	// Use the helper function to handle the SSE stream
	log.Debugf("SSE stream opened for request ID: %v)", request.ID)
	handleSSEStream(ctx, s.corsEnabled, w, flusher, eventsChan, request.ID, tracker, sse.FormatJSONRPCEventBatch)
}

// handleSSEStream handles an SSE stream for a task, including setup and event forwarding.
// It sets the appropriate headers, logs connection status, and forwards events to the client.
func handleSSEStream(
	ctx context.Context,
	corsEnabled bool,
	w http.ResponseWriter,
	flusher http.Flusher,
	eventsChan <-chan protocol.StreamResponse,
	rpcID interface{},
	tracker *metricsTracker,
	formatBatch func(io.Writer, []sse.EventBatch) error,
) {
	if tracker != nil {
		defer tracker.record(ctx)
	}
	if rpcID == nil {
		rpcID = ""
	}
	// Set headers for SSE.
	w.Header().Set("Content-Type", protocol.MediaTypeEventStream)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if corsEnabled {
		setCORSHeaders(w)
	}

	// Indicate successful subscription setup.
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // Send headers immediately.

	if tracker != nil {
		eventsChan = trackStreamEvents(ctx, eventsChan, tracker)
	}

	clientClosed := ctx.Done()
	for {
		select {
		case event, ok := <-eventsChan:
			if !ok {
				return
			}
			eventType := event.EventType()
			if eventType == "" {
				log.Warnf("Unknown event type received for request ID: %v. Skipping.", rpcID)
				continue
			}
			batch := []sse.EventBatch{{
				EventType: eventType,
				ID:        rpcID,
				Data:      &event,
			}}
			if err := formatBatch(w, batch); err != nil {
				log.Errorf("Error writing SSE event for request ID: %v (client likely disconnected): %v", rpcID, err)
				drainToClose(eventsChan)
				return
			}
			flusher.Flush()

		case <-clientClosed:
			log.Infof("SSE client disconnected for request ID: %v. Closing stream.", rpcID)
			drainToClose(eventsChan)
			return
		}
	}
}

// drainToClose keeps consuming the task-manager event channel until it closes,
// discarding the events. The taskmanager pipe must always be drained to
// closure: with a blocking-send manager the drain engine blocks on a full
// pipe, so abandoning it on a client disconnect or write error would wedge the
// execution forever.
func drainToClose(eventsChan <-chan protocol.StreamResponse) {
	go func() {
		for range eventsChan {
		}
	}()
}

func trackStreamEvents(
	ctx context.Context,
	eventsChan <-chan protocol.StreamResponse,
	tracker *metricsTracker,
) <-chan protocol.StreamResponse {
	tracked := make(chan protocol.StreamResponse)
	go func() {
		defer close(tracked)
		for {
			select {
			case <-ctx.Done():
				tracker.setError(errTypeClientDisconnected)
				// Abandoning the source pipe would wedge a blocking-send engine;
				// keep draining it to closure (the downstream loop drains the
				// wrapper channel, not this source).
				drainToClose(eventsChan)
				return
			case event, ok := <-eventsChan:
				if !ok {
					return
				}
				tracker.onEvent(event)
				select {
				case <-ctx.Done():
					tracker.setError(errTypeClientDisconnected)
					drainToClose(eventsChan)
					return
				case tracked <- event:
				}
			}
		}
	}()
	return tracked
}

// handleAgentGetAuthenticatedExtendedCard handles the agent/getAuthenticatedExtendedCard JSON-RPC method.
// This method returns an extended version of the agent card for authenticated users.
func (s *A2AServer) handleAgentGetAuthenticatedExtendedCard(
	ctx context.Context,
	w http.ResponseWriter,
	request jsonrpc.Request,
) {
	var params struct {
		Tenant string `json:"tenant,omitempty"`
	}
	if len(request.Params) != 0 {
		if err := s.unmarshalParams(request.Params, &params); err != nil {
			s.writeJSONRPCError(w, request.ID, err)
			return
		}
	}
	cardToServe, err := s.resolveExtendedAgentCard(ctx, params.Tenant)
	if err != nil {
		log.Warnf("Authenticated extended card unavailable (Request ID: %v): %v", request.ID, err)
		s.handleTaskManagerError(w, request.ID, err, "GetExtendedAgentCard", "")
		return
	}
	log.Debugf("Serving authenticated extended card (Request ID: %v)", request.ID)
	s.writeJSONRPCResponse(w, request.ID, cardToServe)
}
