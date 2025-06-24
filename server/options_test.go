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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
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

func TestWithBasePath(t *testing.T) {
	tests := []struct {
		name              string
		basePath          string
		expectedJSONRPC   string
		expectedAgentCard string
		expectedJWKS      string
	}{
		{
			name:              "Basic sub-path",
			basePath:          "/agent",
			expectedJSONRPC:   "/agent/",
			expectedAgentCard: "/agent/.well-known/agent.json",
			expectedJWKS:      "/agent/.well-known/jwks.json",
		},
		{
			name:              "Multi-level path",
			basePath:          "/api/v2/agents/myagent",
			expectedJSONRPC:   "/api/v2/agents/myagent/",
			expectedAgentCard: "/api/v2/agents/myagent/.well-known/agent.json",
			expectedJWKS:      "/api/v2/agents/myagent/.well-known/jwks.json",
		},
		{
			name:              "Path with trailing slash - should be normalized",
			basePath:          "/agent/api/v2/",
			expectedJSONRPC:   "/agent/api/v2/",
			expectedAgentCard: "/agent/api/v2/.well-known/agent.json",
			expectedJWKS:      "/agent/api/v2/.well-known/jwks.json",
		},
		{
			name:              "Path without leading slash - should be normalized",
			basePath:          "agent/api",
			expectedJSONRPC:   "/agent/api/",
			expectedAgentCard: "/agent/api/.well-known/agent.json",
			expectedJWKS:      "/agent/api/.well-known/jwks.json",
		},
		{
			name:              "Path without leading slash and with trailing slash",
			basePath:          "agent/api/",
			expectedJSONRPC:   "/agent/api/",
			expectedAgentCard: "/agent/api/.well-known/agent.json",
			expectedJWKS:      "/agent/api/.well-known/jwks.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create server instance with default paths
			s := &A2AServer{
				jsonRPCEndpoint: protocol.DefaultJSONRPCPath,
				agentCardPath:   protocol.AgentCardPath,
				jwksEndpoint:    protocol.JWKSPath,
			}

			// Apply WithBasePath option
			opt := WithBasePath(tt.basePath)
			opt(s)

			// Verify all paths are correctly configured
			assert.Equal(t, tt.expectedJSONRPC, s.jsonRPCEndpoint, "JSON-RPC endpoint mismatch")
			assert.Equal(t, tt.expectedAgentCard, s.agentCardPath, "Agent card path mismatch")
			assert.Equal(t, tt.expectedJWKS, s.jwksEndpoint, "JWKS endpoint mismatch")
		})
	}
}

func TestWithBasePathEmptyAndRoot(t *testing.T) {
	// Test with empty string - should not change paths
	s := &A2AServer{
		jsonRPCEndpoint: protocol.DefaultJSONRPCPath,
		agentCardPath:   protocol.AgentCardPath,
		jwksEndpoint:    protocol.JWKSPath,
	}

	opt := WithBasePath("")
	opt(s)

	// Paths should remain unchanged
	assert.Equal(t, protocol.DefaultJSONRPCPath, s.jsonRPCEndpoint)
	assert.Equal(t, protocol.AgentCardPath, s.agentCardPath)
	assert.Equal(t, protocol.JWKSPath, s.jwksEndpoint)

	// Test with root path "/" - should not change paths
	opt = WithBasePath("/")
	opt(s)

	// Paths should remain unchanged
	assert.Equal(t, protocol.DefaultJSONRPCPath, s.jsonRPCEndpoint)
	assert.Equal(t, protocol.AgentCardPath, s.agentCardPath)
	assert.Equal(t, protocol.JWKSPath, s.jwksEndpoint)
}

// mockAuthProvider is a simple mock implementing auth.Provider interface
type mockAuthProvider struct{}

func (p *mockAuthProvider) Authenticate(r *http.Request) (*auth.User, error) {
	return &auth.User{ID: "test-user"}, nil
}
