// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"

	"trpc.group/trpc-go/trpc-a2a-go/v2/auth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/telemetry"
	"trpc.group/trpc-go/trpc-a2a-go/v2/telemetry/metrics"
)

const (
	defaultReadTimeout  = 60 * time.Second
	defaultWriteTimeout = 60 * time.Second
	defaultIdleTimeout  = 300 * time.Second
)

// Middleware is an interface for authentication middlewares.
type Middleware interface {
	Wrap(next http.Handler) http.Handler
}

// MiddlewareChain represents a chain of middlewares that can be composed together.
type MiddlewareChain []Middleware

// Wrap applies all middlewares in the chain to the given handler.
// Middlewares are applied in reverse order so the first middleware in the slice
// becomes the outermost wrapper.
func (chain MiddlewareChain) Wrap(handler http.Handler) http.Handler {
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i].Wrap(handler)
	}
	return handler
}

// Option is a function that configures the A2AServer.
type Option func(*A2AServer)

// WithCORSEnabled enables CORS for the server.
func WithCORSEnabled(enabled bool) Option {
	return func(s *A2AServer) {
		s.corsEnabled = enabled
	}
}

// WithJSONRPCEndpoint sets the path for the JSON-RPC endpoint.
// Default is the root path ("/").
func WithJSONRPCEndpoint(path string) Option {
	return func(s *A2AServer) {
		s.jsonRPCEndpoint = path
		s.pathsExplicitlySet = true
	}
}

// WithCompatHandler installs a fallback handler for JSON-RPC requests whose
// method name is not a v1.0 method (e.g. the legacy slash-delimited names
// served by compat/v0). The handler is mounted on the same JSON-RPC endpoint
// and runs INSIDE the authentication middleware chain, so the legacy protocol
// path is authenticated exactly like the v1 path.
//
// Prefer this over composing compat/v0.NewDualHandler around Server.Handler():
// the latter dispatches before the middleware and therefore bypasses auth.
func WithCompatHandler(h http.Handler) Option {
	return func(s *A2AServer) {
		s.compatHandler = h
	}
}

// WithAgentCard sets the server's default agent card. For a single-agent server
// this is the card served at /.well-known/agent-card.json, and its URL also
// supplies the base path when no path option is set. For a multi-tenant server
// (WithTenantCard / WithTenantCardProvider) it is optional and, when given,
// becomes the default "directory" card served when no "?tenant=" is present.
//
// At least one of WithAgentCard, WithTenantCard or WithTenantCardProvider must be
// supplied to NewA2AServer.
func WithAgentCard(card AgentCard) Option {
	return func(s *A2AServer) {
		// Ensure the served card carries a v1.0-conformant supportedInterfaces list,
		// deriving it from the deprecated URL/PreferredTransport fields when needed.
		card.NormalizeInterfaces()
		s.agentCard = card
		s.agentCardSet = true
	}
}

// WithTenantCard registers an AgentCard for a specific tenant, making the server
// multi-tenant: one process can host several agents distinguished by the v1.0
// tenant field (carried in the request body). The processor dispatches on
// ProcessOptions.Tenant, and the agent-card endpoint serves the matching card for
// "?tenant=<tenant>". This replaces the legacy URL-path template + placeholder
// card + custom AgentCardHandler mechanism. Call once per tenant.
func WithTenantCard(tenant string, card AgentCard) Option {
	return func(s *A2AServer) {
		if s.tenantCards == nil {
			s.tenantCards = make(map[string]AgentCard)
		}
		card.NormalizeInterfaces()
		// Stamp the tenant onto the card's interfaces so clients see who to address.
		for i := range card.SupportedInterfaces {
			card.SupportedInterfaces[i].Tenant = tenant
		}
		s.tenantCards[tenant] = card
	}
}

// WithTenantCardProvider resolves a tenant's AgentCard dynamically (e.g. from a
// database) for when the set of tenants is not known at startup. It is consulted
// after the static WithTenantCard registry; return an error for an unknown tenant.
func WithTenantCardProvider(provider func(ctx context.Context, tenant string) (AgentCard, error)) Option {
	return func(s *A2AServer) {
		s.tenantCardProvider = provider
	}
}

// WithReadTimeout sets the read timeout for the HTTP server.
func WithReadTimeout(timeout time.Duration) Option {
	return func(s *A2AServer) {
		s.readTimeout = timeout
	}
}

// WithWriteTimeout sets the write timeout for the HTTP server.
func WithWriteTimeout(timeout time.Duration) Option {
	return func(s *A2AServer) {
		s.writeTimeout = timeout
	}
}

// WithIdleTimeout sets the idle timeout for the HTTP server.
func WithIdleTimeout(timeout time.Duration) Option {
	return func(s *A2AServer) {
		s.idleTimeout = timeout
	}
}

// WithAuthProvider sets the authentication provider for the server.
// This function converts the auth provider to a middleware and adds it to the middleware chain.
func WithAuthProvider(provider auth.Provider) Option {
	return func(s *A2AServer) {
		// Convert auth provider to middleware and add to chain
		s.middleWare = append(s.middleWare, auth.NewMiddleware(provider))
	}
}

// WithJWKSEndpoint enables the JWKS endpoint for push notification authentication.
// This is used for providing public keys for JWT verification.
// The path defaults to "/.well-known/jwks.json".
func WithJWKSEndpoint(enabled bool, path string) Option {
	return func(s *A2AServer) {
		s.jwksEnabled = enabled
		if path != "" {
			s.jwksEndpoint = path
			s.pathsExplicitlySet = true
		}
	}
}

// WithPushNotificationAuthenticator sets a custom authenticator for push notifications.
// This allows reusing the same authenticator instance throughout the application
// ensuring that the same keys are used for signing and verification.
func WithPushNotificationAuthenticator(authenticator *auth.PushNotificationAuthenticator) Option {
	return func(s *A2AServer) {
		s.pushAuth = authenticator
	}
}

// WithBasePath sets a base path for all A2A endpoints.
// This option has higher priority than agentCard.URL path extraction.
// When provided, it overrides any path extracted from agentCard.URL.
//
// This is useful when:
// - The agentCard.URL is for external/frontend use
// - Internal routing/forwarding maps to different backend paths
// - You need explicit control over the serving endpoints
//
// The base path will be automatically prepended to all standard A2A endpoints:
// - Agent card: basePath + "/.well-known/agent-card.json"
// - JSON-RPC: basePath + "/"
// - JWKS: basePath + "/.well-known/jwks.json"
//
// Example: WithBasePath("/api/v1/agent") creates endpoints:
// - /api/v1/agent/.well-known/agent-card.json
// - /api/v1/agent/
// - /api/v1/agent/.well-known/jwks.json
//
// The base path should start with "/" and not end with "/".
func WithBasePath(basePath string) Option {
	return func(s *A2AServer) {
		// Normalize the base path.
		if basePath == "" || basePath == "/" {
			// Empty or root path - use default paths.
			return
		}

		// Ensure the base path starts with "/".
		if !strings.HasPrefix(basePath, "/") {
			basePath = "/" + basePath
		}

		// Remove trailing slash.
		basePath = strings.TrimSuffix(basePath, "/")

		// Set all endpoint paths with the base path prefix.
		s.pathsExplicitlySet = true
		s.jsonRPCEndpoint = basePath + "/"
		s.agentCardPath = basePath + protocol.AgentCardPath
		s.oldAgentCardPath = basePath + protocol.OldAgentCardPath
		s.jwksEndpoint = basePath + protocol.JWKSPath
	}
}

// WithMiddleware adds HTTP middleware(s) to the server's chain.
// Multiple middlewares can be provided and will be chained together.
// The first middleware in the slice will be the outermost wrapper.
// Middlewares only take effect on the JSON-RPC endpoint.
func WithMiddleware(middlewares ...Middleware) Option {
	return func(s *A2AServer) {
		s.middleWare = append(s.middleWare, middlewares...)
	}
}

// WithAuthenticatedExtendedCardHandler sets a dynamic card handler function that can customize the agent card
func WithAuthenticatedExtendedCardHandler(handler func(ctx context.Context, baseCard AgentCard) (AgentCard, error)) Option {
	return func(s *A2AServer) {
		s.authenticatedCardHandler = handler
	}
}

// WithFirstTokenPolicy sets custom TTFT detection logic for request responses.
func WithFirstTokenPolicy(policy telemetry.FirstTokenPolicy) Option {
	return func(s *A2AServer) {
		s.firstTokenPolicy = policy
	}
}

// WithTelemetryMeterProvider injects an externally managed meter provider.
func WithTelemetryMeterProvider(provider metric.MeterProvider) Option {
	return func(s *A2AServer) {
		s.telemetryMeterProvider = provider
		s.telemetryOptions = nil
		s.telemetryOwnsProvider = false
		s.telemetryShutdown = nil
	}
}

// WithTelemetryMeterProviderOptions configures an internally managed OTLP meter provider.
func WithTelemetryMeterProviderOptions(opts ...metrics.Option) Option {
	return func(s *A2AServer) {
		s.telemetryMeterProvider = nil
		s.telemetryOptions = append([]metrics.Option(nil), opts...)
	}
}
