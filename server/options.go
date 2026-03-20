// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/mikeboe/trpc-a2a-go/auth"
	"github.com/mikeboe/trpc-a2a-go/telemetry"
	"github.com/mikeboe/trpc-a2a-go/telemetry/metrics"
)

const (
	defaultReadTimeout  = 60 * time.Second
	defaultWriteTimeout = 60 * time.Second
	defaultIdleTimeout  = 300 * time.Second
)

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
// If not set, the server will not require authentication.
func WithAuthProvider(provider auth.Provider) Option {
	return func(s *A2AServer) {
		s.authProvider = provider
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

// WithFirstTokenMatcher sets a custom function to determine which event counts as the
// "first token" for time-to-first-token (TTFT) measurement. If not set, the default
// matcher treats the first Working status with a message or the first artifact event
// as the first token in streaming mode, and the response itself in non-streaming mode.
func WithFirstTokenMatcher(matcher telemetry.FirstTokenMatcher) Option {
	return func(s *A2AServer) {
		s.firstTokenMatcher = matcher
	}
}

// WithTelemetryMeterProvider configures the server to initialize A2A metrics
// with the provided OpenTelemetry meter provider when Start is called.
// The provider is treated as externally owned and will not be shut down by the server.
func WithTelemetryMeterProvider(provider metric.MeterProvider) Option {
	return func(s *A2AServer) {
		s.telemetryMeterProvider = provider
	}
}

// WithTelemetryMeterProviderOptions configures the server to create and initialize
// an OTLP-backed meter provider when Start is called.
func WithTelemetryMeterProviderOptions(opts ...metrics.Option) Option {
	return func(s *A2AServer) {
		s.telemetryOptions = append([]metrics.Option(nil), opts...)
	}
}
