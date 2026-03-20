// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/mikeboe/trpc-a2a-go/auth"
	"github.com/mikeboe/trpc-a2a-go/telemetry/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithCORSEnabled(t *testing.T) {
	// Test with CORS enabled
	opt := WithCORSEnabled(true)
	s := &A2AServer{}
	opt(s)
	assert.True(t, s.corsEnabled)

	// Test with CORS disabled
	opt = WithCORSEnabled(false)
	opt(s)
	assert.False(t, s.corsEnabled)
}

func TestWithJSONRPCEndpoint(t *testing.T) {
	// Test with custom JSON-RPC path
	path := "/custom/path"
	opt := WithJSONRPCEndpoint(path)
	s := &A2AServer{}
	opt(s)
	assert.Equal(t, path, s.jsonRPCEndpoint)
}

func TestWithReadTimeout(t *testing.T) {
	// Test with custom read timeout
	timeout := 30 * time.Second
	opt := WithReadTimeout(timeout)
	s := &A2AServer{}
	opt(s)
	assert.Equal(t, timeout, s.readTimeout)
}

func TestWithWriteTimeout(t *testing.T) {
	// Test with custom write timeout
	timeout := 30 * time.Second
	opt := WithWriteTimeout(timeout)
	s := &A2AServer{}
	opt(s)
	assert.Equal(t, timeout, s.writeTimeout)
}

func TestWithIdleTimeout(t *testing.T) {
	// Test with custom idle timeout
	timeout := 120 * time.Second
	opt := WithIdleTimeout(timeout)
	s := &A2AServer{}
	opt(s)
	assert.Equal(t, timeout, s.idleTimeout)
}

func TestWithAuthProvider(t *testing.T) {
	// Create a mock auth provider
	provider := &mockAuthProvider{}

	// Test with auth provider
	opt := WithAuthProvider(provider)
	s := &A2AServer{}
	opt(s)
	assert.Equal(t, provider, s.authProvider)
}

func TestWithJWKSEndpoint(t *testing.T) {
	// Test with JWKS endpoint enabled and custom path
	customPath := "/custom/jwks.json"
	opt := WithJWKSEndpoint(true, customPath)
	s := &A2AServer{}
	opt(s)
	assert.True(t, s.jwksEnabled)
	assert.Equal(t, customPath, s.jwksEndpoint)

	// Test with JWKS endpoint disabled
	opt = WithJWKSEndpoint(false, "")
	opt(s)
	assert.False(t, s.jwksEnabled)
}

// Test for WithPushNotificationAuthenticator option
func TestWithPushNotificationAuthenticator(t *testing.T) {
	authenticator := auth.NewPushNotificationAuthenticator()
	require.NoError(t, authenticator.GenerateKeyPair())

	serverOptions := &A2AServer{}
	opt := WithPushNotificationAuthenticator(authenticator)
	opt(serverOptions)

	assert.Equal(t, authenticator, serverOptions.pushAuth)
}

func TestWithTelemetryMeterProvider(t *testing.T) {
	provider := noop.NewMeterProvider()

	serverOptions := &A2AServer{}
	opt := WithTelemetryMeterProvider(provider)
	opt(serverOptions)

	assert.Equal(t, provider, serverOptions.telemetryMeterProvider)
}

func TestWithTelemetryMeterProviderOptions(t *testing.T) {
	serverOptions := &A2AServer{}
	opt := WithTelemetryMeterProviderOptions(
		metrics.WithProtocol("http"),
		metrics.WithEndpoint("localhost:4318"),
	)
	opt(serverOptions)

	assert.Len(t, serverOptions.telemetryOptions, 2)
}

// mockAuthProvider is a simple mock implementing auth.Provider interface
type mockAuthProvider struct{}

func (p *mockAuthProvider) Authenticate(r *http.Request) (*auth.User, error) {
	return &auth.User{ID: "test-user"}, nil
}
