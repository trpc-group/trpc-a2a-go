// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func TestWithHTTPClient(t *testing.T) {
	// Create a custom HTTP client with specific timeout
	customClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Create A2A client with the custom HTTP client
	client, err := NewA2AClient("http://localhost:8080", WithHTTPClient(customClient))
	require.NoError(t, err)

	// Verify the client is using our custom HTTP client
	assert.Equal(t, customClient, client.httpClient)
	assert.Equal(t, 30*time.Second, client.httpClient.Timeout)

	// Test with nil client (should use default)
	defaultClient, err := NewA2AClient("http://localhost:8080", WithHTTPClient(nil))
	require.NoError(t, err)
	assert.NotNil(t, defaultClient.httpClient)
	assert.EqualValues(t, defaultTimeout, defaultClient.httpClient.Timeout)
}

func TestWithTimeout(t *testing.T) {
	// Create client with custom timeout
	client, err := NewA2AClient("http://localhost:8080", WithTimeout(45*time.Second))
	require.NoError(t, err)

	// Verify timeout was set correctly
	assert.Equal(t, 45*time.Second, client.httpClient.Timeout)

	// Test with zero timeout (should use default)
	defaultClient, err := NewA2AClient("http://localhost:8080", WithTimeout(0))
	require.NoError(t, err)
	assert.EqualValues(t, defaultTimeout, defaultClient.httpClient.Timeout)
}

func TestWithUserAgent(t *testing.T) {
	// Create client with custom user agent
	customUA := "CustomUserAgent/1.0"
	client, err := NewA2AClient("http://localhost:8080", WithUserAgent(customUA))
	require.NoError(t, err)

	// Verify user agent was set correctly
	assert.Equal(t, customUA, client.userAgent)
}

func TestWithJWTAuth(t *testing.T) {
	secretKey := []byte("test-secret-key")
	audience := "test-audience"
	issuer := "test-issuer"
	lifetime := 1 * time.Hour

	// Create client with JWT auth
	client, err := NewA2AClient("http://localhost:8080",
		WithJWTAuth(secretKey, audience, issuer, lifetime))
	require.NoError(t, err)

	// Verify auth provider was set up correctly
	assert.NotNil(t, client.authProvider)

	// Type assertion to check it's the right type
	jwtProvider, ok := client.authProvider.(*auth.JWTAuthProvider)
	assert.True(t, ok, "Should be a JWTAuthProvider")
	assert.Equal(t, secretKey, jwtProvider.Secret)
	assert.Equal(t, audience, jwtProvider.Audience)
	assert.Equal(t, issuer, jwtProvider.Issuer)
	assert.Equal(t, lifetime, jwtProvider.TokenLifetime)
}

func TestWithAPIKeyAuth(t *testing.T) {
	apiKey := "test-api-key"
	headerName := "X-Custom-API-Key"

	// Create client with API key auth
	client, err := NewA2AClient("http://localhost:8080",
		WithAPIKeyAuth(apiKey, headerName))
	require.NoError(t, err)

	// Verify auth provider was set up correctly
	assert.NotNil(t, client.authProvider)

	// Type assertion to check it's the right type
	_, ok := client.authProvider.(*auth.APIKeyAuthProvider)
	assert.True(t, ok, "Should be an APIKeyAuthProvider")
}

func TestWithOAuth2ClientCredentials(t *testing.T) {
	clientID := "test-client-id"
	clientSecret := "test-client-secret-placeholder"
	tokenURL := "https://auth.example.com/token"
	scopes := []string{"profile", "email"}

	// Create client with OAuth2 client credentials
	client, err := NewA2AClient("http://localhost:8080",
		WithOAuth2ClientCredentials(clientID, clientSecret, tokenURL, scopes))
	require.NoError(t, err)

	// Verify auth provider was set up correctly
	assert.NotNil(t, client.authProvider)

	// Type assertion to check it's the right type
	_, ok := client.authProvider.(*auth.OAuth2AuthProvider)
	assert.True(t, ok, "Should be an OAuth2AuthProvider")
}

func TestWithOAuth2TokenSource(t *testing.T) {
	// Create a test OAuth2 config
	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret-placeholder",
		Endpoint: oauth2.Endpoint{
			TokenURL: "https://auth.example.com/token",
		},
	}

	// Create a static token source
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "test-access-token-placeholder",
		TokenType:   "Bearer",
	})

	// Create client with OAuth2 token source
	client, err := NewA2AClient("http://localhost:8080",
		WithOAuth2TokenSource(config, tokenSource))
	require.NoError(t, err)

	// Verify auth provider was set up correctly
	assert.NotNil(t, client.authProvider)

	// Type assertion to check it's the right type
	_, ok := client.authProvider.(*auth.OAuth2AuthProvider)
	assert.True(t, ok, "Should be an OAuth2AuthProvider")
}

func TestWithAuthProvider(t *testing.T) {
	// Create a mock auth provider
	mockProvider := &mockClientProvider{}

	// Create client with custom auth provider
	client, err := NewA2AClient("http://localhost:8080",
		WithAuthProvider(mockProvider))
	require.NoError(t, err)

	// Verify auth provider was set correctly
	assert.Equal(t, mockProvider, client.authProvider)
}

// mockClientProvider implements auth.ClientProvider for testing
type mockClientProvider struct{}

func (p *mockClientProvider) Authenticate(r *http.Request) (*auth.User, error) {
	return &auth.User{ID: "mock-user"}, nil
}

func (p *mockClientProvider) ConfigureClient(client *http.Client) *http.Client {
	return client
}

// TestRequestOptions_CustomHeaders tests that custom headers are properly set on requests
func TestRequestOptions_CustomHeaders(t *testing.T) {
	// Track received headers
	var receivedHeaders http.Header

	// Create a test server that captures headers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()

		// Parse the request to get the method
		body, _ := io.ReadAll(r.Body)
		var req jsonrpc.Request
		json.Unmarshal(body, &req)

		// Return a valid response based on the method
		result := protocol.Task{
			ID:        "test-task-id",
			ContextID: "test-context-id",
			Kind:      protocol.KindTask,
			Status: protocol.TaskStatus{
				State: protocol.TaskStateCompleted,
			},
		}
		resultBytes, _ := json.Marshal(result)

		// Create JSON-RPC response with raw JSON result
		responseMap := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  json.RawMessage(resultBytes),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responseMap)
	}))
	defer server.Close()

	// Create client
	client, err := NewA2AClient(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	t.Run("WithRequestHeader - Single Header", func(t *testing.T) {
		receivedHeaders = nil

		params := protocol.SendMessageParams{
			RPCID: "test-1",
			Message: protocol.Message{
				MessageID: "msg-1",
				Role:      protocol.MessageRoleUser,
				Kind:      protocol.KindMessage,
				Parts: []protocol.Part{
					protocol.TextPart{
						Kind: protocol.KindText,
						Text: "test message",
					},
				},
			},
		}

		_, err := client.SendMessage(
			context.Background(),
			params,
			WithRequestHeader("X-Custom-Header", "custom-value"),
		)
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		if receivedHeaders == nil {
			t.Fatal("No headers received")
		}

		if got := receivedHeaders.Get("X-Custom-Header"); got != "custom-value" {
			t.Errorf("Expected X-Custom-Header=custom-value, got %s", got)
		}
	})

	t.Run("WithRequestHeaders - Multiple Headers", func(t *testing.T) {
		receivedHeaders = nil

		params := protocol.SendMessageParams{
			RPCID: "test-2",
			Message: protocol.Message{
				MessageID: "msg-2",
				Role:      protocol.MessageRoleUser,
				Kind:      protocol.KindMessage,
				Parts: []protocol.Part{
					protocol.TextPart{
						Kind: protocol.KindText,
						Text: "test message",
					},
				},
			},
		}

		customHeaders := map[string]string{
			"X-Request-ID":  "req-123",
			"X-Tenant-ID":   "tenant-456",
			"X-Custom-Auth": "Bearer token123",
		}

		_, err := client.SendMessage(
			context.Background(),
			params,
			WithRequestHeaders(customHeaders),
		)
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		if receivedHeaders == nil {
			t.Fatal("No headers received")
		}

		for key, expectedValue := range customHeaders {
			if got := receivedHeaders.Get(key); got != expectedValue {
				t.Errorf("Expected %s=%s, got %s", key, expectedValue, got)
			}
		}
	})

	t.Run("Multiple RequestOptions", func(t *testing.T) {
		receivedHeaders = nil

		params := protocol.SendMessageParams{
			RPCID: "test-3",
			Message: protocol.Message{
				MessageID: "msg-3",
				Role:      protocol.MessageRoleUser,
				Kind:      protocol.KindMessage,
				Parts: []protocol.Part{
					protocol.TextPart{
						Kind: protocol.KindText,
						Text: "test message",
					},
				},
			},
		}

		_, err := client.SendMessage(
			context.Background(),
			params,
			WithRequestHeader("X-Header-1", "value-1"),
			WithRequestHeader("X-Header-2", "value-2"),
			WithRequestHeaders(map[string]string{
				"X-Header-3": "value-3",
				"X-Header-4": "value-4",
			}),
		)
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		if receivedHeaders == nil {
			t.Fatal("No headers received")
		}

		expectedHeaders := map[string]string{
			"X-Header-1": "value-1",
			"X-Header-2": "value-2",
			"X-Header-3": "value-3",
			"X-Header-4": "value-4",
		}

		for key, expectedValue := range expectedHeaders {
			if got := receivedHeaders.Get(key); got != expectedValue {
				t.Errorf("Expected %s=%s, got %s", key, expectedValue, got)
			}
		}
	})

	t.Run("GetTasks with Custom Headers", func(t *testing.T) {
		receivedHeaders = nil

		params := protocol.TaskQueryParams{
			RPCID: "test-4",
			ID:    "test-task-id",
		}

		_, err := client.GetTasks(
			context.Background(),
			params,
			WithRequestHeader("X-Trace-ID", "trace-789"),
		)
		if err != nil {
			t.Fatalf("GetTasks failed: %v", err)
		}

		if receivedHeaders == nil {
			t.Fatal("No headers received")
		}

		if got := receivedHeaders.Get("X-Trace-ID"); got != "trace-789" {
			t.Errorf("Expected X-Trace-ID=trace-789, got %s", got)
		}
	})

	t.Run("Standard Headers Not Overridden", func(t *testing.T) {
		receivedHeaders = nil

		params := protocol.SendMessageParams{
			RPCID: "test-5",
			Message: protocol.Message{
				MessageID: "msg-5",
				Role:      protocol.MessageRoleUser,
				Kind:      protocol.KindMessage,
				Parts: []protocol.Part{
					protocol.TextPart{
						Kind: protocol.KindText,
						Text: "test message",
					},
				},
			},
		}

		_, err := client.SendMessage(
			context.Background(),
			params,
			WithRequestHeader("X-Custom", "value"),
		)
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		if receivedHeaders == nil {
			t.Fatal("No headers received")
		}

		// Verify standard headers are still set
		if got := receivedHeaders.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("Expected Content-Type=application/json; charset=utf-8, got %s", got)
		}

		if got := receivedHeaders.Get("Accept"); got != "application/json" {
			t.Errorf("Expected Accept=application/json, got %s", got)
		}
	})
}

// TestRequestOptions_StreamingMethods tests custom headers work with streaming methods
func TestRequestOptions_StreamingMethods(t *testing.T) {
	var receivedHeaders http.Header

	// Create a test server for SSE
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send a simple event and close
		flusher, _ := w.(http.Flusher)
		w.Write([]byte("event: message\ndata: {\"type\":\"message\",\"message\":\"test\"}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client, err := NewA2AClient(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	t.Run("StreamMessage with Custom Headers", func(t *testing.T) {
		receivedHeaders = nil

		params := protocol.SendMessageParams{
			RPCID: "stream-test-1",
			Message: protocol.Message{
				MessageID: "msg-stream-1",
				Role:      protocol.MessageRoleUser,
				Kind:      protocol.KindMessage,
				Parts: []protocol.Part{
					protocol.TextPart{
						Kind: protocol.KindText,
						Text: "test message",
					},
				},
			},
		}

		eventsChan, err := client.StreamMessage(
			context.Background(),
			params,
			WithRequestHeader("X-Stream-ID", "stream-123"),
		)
		if err != nil {
			t.Fatalf("StreamMessage failed: %v", err)
		}

		// Consume the channel
		for range eventsChan {
			// Just drain the channel
		}

		if receivedHeaders == nil {
			t.Fatal("No headers received")
		}

		if got := receivedHeaders.Get("X-Stream-ID"); got != "stream-123" {
			t.Errorf("Expected X-Stream-ID=stream-123, got %s", got)
		}

		// Verify Accept header is still set to text/event-stream
		if got := receivedHeaders.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Expected Accept=text/event-stream, got %s", got)
		}
	})
}
