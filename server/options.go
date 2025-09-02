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

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

const (
	defaultReadTimeout  = 60 * time.Second
	defaultWriteTimeout = 60 * time.Second
	defaultIdleTimeout  = 300 * time.Second
	// todo: remove this in next version
	oldAgentCardPath = "/.well-known/agent.json"
)

// Middleware is an interface for authentication middlewares.
type Middleware interface {
	Wrap(next http.Handler) http.Handler
}

// MiddlewareChain represents a chain of middlewares that can be composed together.
type MiddlewareChain []Middleware

// HTTPRouter represents a router for HTTP requests.
type HTTPRouter interface {
	Handle(pattern string, handler http.Handler)
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

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
		s.jsonRPCEndpoint = basePath + "/"
		s.agentCardPath = basePath + protocol.AgentCardPath
		s.oldAgentCardPath = basePath + oldAgentCardPath
		s.jwksEndpoint = basePath + protocol.JWKSPath
	}
}

// WithMiddleWare sets the authentication middleware for the server.
// Multiple middlewares can be provided and will be chained together.
// The first middleware in the slice will be the outermost wrapper.
// Middlewares only take effect on JSON-RPC endpoint.
func WithMiddleWare(middlewares ...Middleware) Option {
	return func(s *A2AServer) {
		s.middleWare = append(s.middleWare, middlewares...)
	}
}

// WithAgentCardHandler sets the handler for the agent card endpoint.
func WithAgentCardHandler(handler http.Handler) Option {
	return func(s *A2AServer) {
		s.agentCardHandler = handler
	}
}

// WithHTTPRouter sets the custom HTTP router for the server.
// This allows for advanced routing features like multi-agent support, path parameters, and wildcards.
// Example: WithHTTPRouter(mux.NewRouter()) for Gorilla Mux support.
func WithHTTPRouter(router HTTPRouter) Option {
	return func(s *A2AServer) {
		s.customRouter = router
	}
}

// WithExtendedAgentCard sets an extended agent card that will be returned to authenticated users.
// This card can contain additional information not available in the public agent card.
// When set, the agent will automatically set SupportsAuthenticatedExtendedCard to true.
func WithExtendedAgentCard(extendedCard AgentCard) Option {
	return func(s *A2AServer) {
		s.extendedAgentCard = &extendedCard
		// Automatically enable support for authenticated extended card
		s.agentCard.SupportsAuthenticatedExtendedCard = &[]bool{true}[0]
	}
}

// WithCardModifier sets a dynamic card modifier function that can customize the agent card
// based on the request context. This allows for per-user or per-request customization.
// The modifier function receives the base card and context, and returns a modified card.
// If both WithExtendedAgentCard and WithCardModifier are used, the modifier will receive
// the extended card as the base card.
func WithCardModifier(modifier func(baseCard AgentCard, ctx context.Context) (AgentCard, error)) Option {
	return func(s *A2AServer) {
		s.cardModifier = modifier
		// Automatically enable support for authenticated extended card
		s.agentCard.SupportsAuthenticatedExtendedCard = &[]bool{true}[0]
	}
}
