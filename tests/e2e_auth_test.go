// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package tests contains end-to-end tests for the A2A protocol.
package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// TestJWTAuthentication tests the JWT authentication mechanism.
func TestJWTAuthentication(t *testing.T) {
	// Generate a random JWT secret
	jwtSecret := make([]byte, 32)
	_, err := rand.Read(jwtSecret)
	require.NoError(t, err, "Failed to generate JWT secret")

	// Create JWT auth provider
	jwtProvider := auth.NewJWTAuthProvider(
		jwtSecret,
		"test-audience",
		"test-issuer",
		1*time.Hour,
	)

	// Create and start the server
	taskMgr, server := setupAuthServer(t, jwtProvider)
	defer server.Close()

	// Create a user token
	token, err := jwtProvider.CreateToken("test-user", nil)
	require.NoError(t, err, "Failed to create JWT token")

	// Create a client with no authentication
	basicClient, err := client.NewA2AClient(server.URL)
	require.NoError(t, err, "Failed to create A2A client")

	// Test that unauthenticated requests fail
	ctx := context.Background()
	_, err = basicClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello, World!"),
	})
	assert.Error(t, err, "Unauthenticated request should fail")
	assert.Contains(t, err.Error(), "401", "Expected 401 Unauthorized")

	// Create a transport that adds the JWT token
	transport := &authRoundTripper{
		base:         http.DefaultTransport,
		authToken:    token,
		tokenType:    "Bearer",
		headerName:   "Authorization",
		headerFormat: "%s %s",
	}

	// Create an authenticated client
	authClient, err := client.NewA2AClient(
		server.URL,
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	require.NoError(t, err, "Failed to create authenticated A2A client")

	// Test that authenticated requests succeed
	messageResult, err := authClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello, World!"),
	})

	require.NoError(t, err, "Authenticated request failed")
	require.NotNil(t, messageResult.Message, "Message result should not be nil")

	resultMessage := messageResult.Message
	processedMessage, exists := taskMgr.(*mockTaskManager).messages[resultMessage.MessageID]
	require.True(t, exists, "Failed to get processed message")
	assert.NotNil(t, processedMessage, "Processed message should not be nil")
}

// TestAPIKeyAuthentication tests the API key authentication mechanism.
func TestAPIKeyAuthentication(t *testing.T) {
	// Create API key auth provider
	apiKeys := map[string]string{
		"valid-api-key": "api-user",
	}
	apiKeyProvider := auth.NewAPIKeyAuthProvider(apiKeys, "X-API-Key")

	// Create and start the server
	taskMgr, server := setupAuthServer(t, apiKeyProvider)
	defer server.Close()

	// Create a client with no authentication
	basicClient, err := client.NewA2AClient(server.URL)
	require.NoError(t, err, "Failed to create A2A client")

	// Test that unauthenticated requests fail
	ctx := context.Background()
	_, err = basicClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello from API key test!"),
	})
	assert.Error(t, err, "Unauthenticated request should fail")
	assert.Contains(t, err.Error(), "401", "Expected 401 Unauthorized")

	// Create a transport that adds the API key
	transport := &authRoundTripper{
		base:         http.DefaultTransport,
		authToken:    "valid-api-key",
		headerName:   "X-API-Key",
		headerFormat: "%s",
	}

	// Create an authenticated client
	authClient, err := client.NewA2AClient(
		server.URL,
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	require.NoError(t, err, "Failed to create authenticated A2A client")

	// Test that authenticated requests succeed
	messageResult, err := authClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello from API key test!"),
	})
	require.NoError(t, err, "Authenticated request failed")
	require.NotNil(t, messageResult.Message, "Message result should not be nil")

	resultMessage := messageResult.Message
	processedMessage, exists := taskMgr.(*mockTaskManager).messages[resultMessage.MessageID]
	require.True(t, exists, "Failed to get processed message")
	assert.NotNil(t, processedMessage, "Processed message should not be nil")
}

// TestChainAuthentication tests that the chain auth provider works with multiple auth methods.
func TestChainAuthentication(t *testing.T) {
	// Generate a random JWT secret
	jwtSecret := make([]byte, 32)
	_, err := rand.Read(jwtSecret)
	require.NoError(t, err, "Failed to generate JWT secret")

	// Create JWT auth provider
	jwtProvider := auth.NewJWTAuthProvider(
		jwtSecret,
		"test-audience",
		"test-issuer",
		1*time.Hour,
	)

	// Create API key auth provider
	apiKeys := map[string]string{
		"chain-api-key": "chain-user",
	}
	apiKeyProvider := auth.NewAPIKeyAuthProvider(apiKeys, "X-API-Key")

	// Create a chain provider with both auth methods
	chainProvider := auth.NewChainAuthProvider(jwtProvider, apiKeyProvider)

	// Create and start the server
	_, server := setupAuthServer(t, chainProvider)
	defer server.Close()

	// Create a client with no authentication
	basicClient, err := client.NewA2AClient(server.URL)
	require.NoError(t, err, "Failed to create A2A client")

	// Test that unauthenticated requests fail
	ctx := context.Background()
	_, err = basicClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello from chain auth test!"),
	})
	assert.Error(t, err, "Unauthenticated request should fail")
	assert.Contains(t, err.Error(), "401", "Expected 401 Unauthorized")

	// Test with JWT authentication
	token, err := jwtProvider.CreateToken("jwt-user", nil)
	require.NoError(t, err, "Failed to create JWT token")

	jwtTransport := &authRoundTripper{
		base:         http.DefaultTransport,
		authToken:    token,
		tokenType:    "Bearer",
		headerName:   "Authorization",
		headerFormat: "%s %s",
	}

	jwtClient, err := client.NewA2AClient(
		server.URL,
		client.WithHTTPClient(&http.Client{Transport: jwtTransport}),
	)
	require.NoError(t, err, "Failed to create JWT authenticated client")

	messageResult, err := jwtClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello with JWT auth!"),
	})
	require.NoError(t, err, "JWT authenticated request failed")
	assert.NotNil(t, messageResult.Message, "Message result should not be nil")

	// Test with API key authentication
	apiKeyTransport := &authRoundTripper{
		base:         http.DefaultTransport,
		authToken:    "chain-api-key",
		headerName:   "X-API-Key",
		headerFormat: "%s",
	}

	apiKeyClient, err := client.NewA2AClient(
		server.URL,
		client.WithHTTPClient(&http.Client{Transport: apiKeyTransport}),
	)
	require.NoError(t, err, "Failed to create API key authenticated client")

	messageResult2, err := apiKeyClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: createTextMessage("Hello with API key auth!"),
	})
	require.NoError(t, err, "API key authenticated request failed")
	assert.NotNil(t, messageResult2.Message, "Message result should not be nil")
}

// TestPushNotificationAuthentication tests push notification authentication.
func TestPushNotificationAuthentication(t *testing.T) {
	// This test simulates a complete flow:
	// 1. Agent server generates keys and exposes JWKS endpoint
	// 2. Client server sets up to receive notifications and verifies them
	// 3. Agent sends authenticated push notification to client

	// Setup agent side (sender)
	// -----------------------
	agentTaskMgr := newMockTaskManager(nil)
	agentAuthenticator := auth.NewPushNotificationAuthenticator()
	err := agentAuthenticator.GenerateKeyPair()
	require.NoError(t, err, "Agent failed to generate key pair")

	agentCard := server.AgentCard{
		Name:    "Push Auth Test Agent",
		URL:     "http://localhost:8080",
		Version: "1.0.0",
		Capabilities: server.AgentCapabilities{
			PushNotifications: &[]bool{true}[0],
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	// Create agent server with JWKS endpoint
	agentServer, err := server.NewA2AServer(
		agentCard,
		agentTaskMgr,
		server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
	)
	require.NoError(t, err, "Failed to create agent server")

	// Set the authenticator in the agent server
	agentServerHandler := http.NewServeMux()

	// Add the JWKS endpoint handler
	agentServerHandler.HandleFunc("/.well-known/jwks.json", agentAuthenticator.HandleJWKS)

	// Add all other A2A API handlers
	agentServerHandler.Handle("/", agentServer.Handler())

	// Start the agent server
	agentHTTPServer := httptest.NewServer(agentServerHandler)
	defer agentHTTPServer.Close()
	agentURL := agentHTTPServer.URL
	agentJWKSURL := fmt.Sprintf("%s/.well-known/jwks.json", agentURL)

	// Verify agent's JWKS endpoint works
	jwksResp, err := http.Get(agentJWKSURL)
	require.NoError(t, err, "Failed to access agent's JWKS endpoint")
	defer jwksResp.Body.Close()
	require.Equal(t, http.StatusOK, jwksResp.StatusCode, "Agent's JWKS endpoint should return 200 OK")

	jwksBody, err := io.ReadAll(jwksResp.Body)
	require.NoError(t, err, "Failed to read agent's JWKS response")
	t.Logf("Agent's JWKS Response: %s", string(jwksBody))

	// Setup client side (receiver)
	// --------------------------
	// Create client authenticator for verification
	clientAuthenticator := auth.NewPushNotificationAuthenticator()
	clientAuthenticator.SetJWKSClient(agentJWKSURL)

	// Channel to track if authentication succeeded
	authSuccessCh := make(chan bool, 1)

	// Set up client server to receive notifications
	clientHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Client received request with headers: %v", r.Header)

		// Read and log the request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Logf("Failed to read request body: %v", err)
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}
		t.Logf("Client received request body: %s", string(body))

		// Verify the push notification JWT
		err = clientAuthenticator.VerifyPushNotification(r, body)
		if err != nil {
			t.Logf("Authentication failed: %v", err)
			http.Error(w, fmt.Sprintf("Authentication failed: %v", err), http.StatusUnauthorized)
			authSuccessCh <- false
			return
		}

		// Authentication succeeded
		authSuccessCh <- true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	clientServer := httptest.NewServer(clientHandler)
	defer clientServer.Close()
	clientURL := clientServer.URL

	// Create a test notification payload
	payload := []byte(`{"message": "test-notification"}`)

	// Create a push notification from agent to client
	// ----------------------------------------------
	// Create authorization header for the notification
	authHeader, err := agentAuthenticator.CreateAuthorizationHeader(payload)
	require.NoError(t, err, "Failed to create authorization header")
	t.Logf("Authorization header created: %s", authHeader)

	// Send push notification from agent to client
	// Validate URL to prevent potential SSRF attacks
	parsedURL, err := url.Parse(clientURL)
	require.NoError(t, err, "Failed to parse client URL")

	// Check if URL is valid - limited to http/https and certain hosts
	require.True(t, parsedURL.Scheme == "http" || parsedURL.Scheme == "https",
		"URL must use http or https scheme")

	// Create request with validated URL
	req, err := http.NewRequest("POST", clientURL, bytes.NewReader(payload))
	require.NoError(t, err, "Failed to create request")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)

	t.Logf("Sending push notification from agent to client...")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "Failed to send push notification")
	defer resp.Body.Close()

	// Read and log response
	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("Client response: Status=%d, Body=%s", resp.StatusCode, string(respBody))

	// Verify authentication succeeded
	var authSuccess bool
	select {
	case authSuccess = <-authSuccessCh:
		// Got result
	case <-time.After(2 * time.Second):
		authSuccess = false
		t.Log("Timed out waiting for authentication result")
	}

	// Check results
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Push notification request should succeed")
	assert.True(t, authSuccess, "Authentication should succeed")

	// Test with invalid token
	// ---------------------
	t.Log("Testing with invalid authorization header...")

	// Reuse the validated URL from earlier
	invalidReq, err := http.NewRequest("POST", clientURL, bytes.NewReader(payload))
	require.NoError(t, err, "Failed to create invalid request")
	invalidReq.Header.Set("Content-Type", "application/json")
	invalidReq.Header.Set("Authorization", "Bearer invalidtoken")

	invalidResp, err := http.DefaultClient.Do(invalidReq)
	require.NoError(t, err, "Failed to send invalid request")
	defer invalidResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, invalidResp.StatusCode, "Invalid token should be rejected")
}

// Helper functions and types below

// authRoundTripper is an http.RoundTripper that adds authentication to requests.
type authRoundTripper struct {
	base         http.RoundTripper
	authToken    string
	tokenType    string
	headerName   string
	headerFormat string
}

// RoundTrip implements http.RoundTripper.
func (t *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	reqClone := req.Clone(req.Context())

	// Add authentication header
	if t.tokenType != "" {
		reqClone.Header.Set(t.headerName, fmt.Sprintf(t.headerFormat, t.tokenType, t.authToken))
	} else {
		reqClone.Header.Set(t.headerName, fmt.Sprintf(t.headerFormat, t.authToken))
	}

	// Pass the request to the base transport
	return t.base.RoundTrip(reqClone)
}

// createTextMessage creates a simple text message for testing.
func createTextMessage(text string) protocol.Message {
	return protocol.Message{
		MessageID: fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Role:      protocol.MessageRoleUser,
		Parts: []*protocol.Part{
			protocol.NewTextPart(text),
		},
	}
}

// setupAuthServer creates and starts a test server with the provided auth provider.
func setupAuthServer(t *testing.T, provider auth.Provider) (taskmanager.TaskManager, *httptest.Server) {
	taskProcessor := &echoProcessor{}
	taskMgr := newMockTaskManager(taskProcessor)

	agentCard := server.AgentCard{
		Name:    "Auth Test Server",
		URL:     "http://localhost:8080",
		Version: "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming: &[]bool{true}[0],
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	a2aServer, err := server.NewA2AServer(
		agentCard,
		taskMgr,
		server.WithAuthProvider(provider),
	)
	require.NoError(t, err, "Failed to create A2A server")

	server := httptest.NewServer(a2aServer.Handler())
	return taskMgr, server
}

// mockTaskManager is a simple implementation of the TaskManager interface.
type mockTaskManager struct {
	processor   taskmanager.MessageProcessor
	tasks       map[string]*protocol.Task
	pushConfigs map[string]protocol.TaskPushNotificationConfig
	messages    map[string]*protocol.Message
}

var _ taskmanager.TaskManager = (*mockTaskManager)(nil)

// newMockTaskManager creates a new mock task manager.
func newMockTaskManager(processor taskmanager.MessageProcessor) *mockTaskManager {
	return &mockTaskManager{
		processor:   processor,
		tasks:       make(map[string]*protocol.Task),
		pushConfigs: make(map[string]protocol.TaskPushNotificationConfig),
		messages:    make(map[string]*protocol.Message),
	}
}

// Task returns a task with the given ID.
func (m *mockTaskManager) Task(id string) (*protocol.Task, error) {
	task, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found", id)
	}
	return task, nil
}

// OnSendMessage handles a request corresponding to the 'message/send' RPC method.
func (m *mockTaskManager) OnSendMessage(
	ctx context.Context, request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	m.messages[request.Message.MessageID] = &request.Message

	return protocol.NewSendMessageResponseMessage(&request.Message), nil
}

// OnSendMessageStream handles a request corresponding to the 'message/stream' RPC method.
func (m *mockTaskManager) OnSendMessageStream(
	ctx context.Context, request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	m.messages[request.Message.MessageID] = &request.Message

	eventCh := make(chan protocol.StreamResponse, 10)

	go func() {
		defer close(eventCh)

		event := protocol.NewStreamResponseMessage(&request.Message)

		select {
		case eventCh <- event:
		case <-ctx.Done():
		}
	}()

	return eventCh, nil
}

// OnGetTask handles getting a task.
func (m *mockTaskManager) OnGetTask(
	ctx context.Context, params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	return m.Task(params.ID)
}

// OnPushNotificationSet sets a push notification configuration for a task.
func (m *mockTaskManager) OnPushNotificationSet(
	ctx context.Context, params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	_, err := m.Task(params.TaskID)
	if err != nil {
		return nil, err
	}

	m.pushConfigs[params.TaskID] = params
	return &params, nil
}

// OnPushNotificationGet gets a push notification configuration for a task.
func (m *mockTaskManager) OnPushNotificationGet(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	_, err := m.Task(params.ID)
	if err != nil {
		return nil, err
	}

	config, ok := m.pushConfigs[params.ID]
	if !ok {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}

	config.TaskID = params.ID
	return &config, nil
}

// OnListTasks implements the v1.0 ListTasks method.
func (m *mockTaskManager) OnListTasks(
	ctx context.Context, params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	return &protocol.ListTasksResult{Tasks: []*protocol.Task{}}, nil
}

// OnPushNotificationList implements the v1.0 ListTaskPushNotificationConfigs method.
func (m *mockTaskManager) OnPushNotificationList(
	ctx context.Context, params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	result := &protocol.ListTaskPushNotificationConfigsResult{
		Configs: []protocol.TaskPushNotificationConfig{},
	}
	if config, ok := m.pushConfigs[params.TaskID]; ok {
		config.TaskID = params.TaskID
		result.Configs = append(result.Configs, config)
	}
	return result, nil
}

// OnPushNotificationDelete implements the v1.0 DeleteTaskPushNotificationConfig method.
func (m *mockTaskManager) OnPushNotificationDelete(
	ctx context.Context, params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	delete(m.pushConfigs, params.TaskID)
	return nil
}

// OnResubscribe handles resubscribing to a task.
func (m *mockTaskManager) OnResubscribe(
	ctx context.Context, params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	task, err := m.Task(params.ID)
	if err != nil {
		return nil, err
	}

	eventCh := make(chan protocol.StreamResponse)

	go func() {
		defer close(eventCh)

		event := protocol.TaskStatusUpdateEvent{
			TaskID: task.ID,
			Status: task.Status,
			Final:  true,
		}

		select {
		case eventCh <- protocol.NewStreamResponseStatusUpdate(&event):
		case <-ctx.Done():
		}
	}()

	return eventCh, nil
}

// OnCancelTask handles canceling a task.
func (m *mockTaskManager) OnCancelTask(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.Task, error) {
	task, err := m.Task(params.ID)
	if err != nil {
		return nil, err
	}

	handle := &mockTaskHandle{
		taskID:  params.ID,
		manager: m,
	}

	if err := handle.UpdateStatus(protocol.TaskStateCanceled, nil); err != nil {
		return task, err
	}

	return m.tasks[params.ID], nil
}

// mockTaskHandle implements the TaskHandle interface.
type mockTaskHandle struct {
	taskID  string
	manager *mockTaskManager
}

// UpdateStatus updates the status of a task.
func (h *mockTaskHandle) UpdateStatus(state protocol.TaskState, message *protocol.Message) error {
	task, e := h.manager.Task(h.taskID)
	if e != nil {
		return e
	}

	task.Status.State = state
	if message != nil {
		task.Status.Message = message
	}

	h.manager.tasks[h.taskID] = task
	return nil
}

// AddArtifact implements the TaskHandle interface.
func (h *mockTaskHandle) AddArtifact(artifact protocol.Artifact) error {
	task, err := h.manager.Task(h.taskID)
	if err != nil {
		return err
	}

	task.Artifacts = append(task.Artifacts, artifact)
	h.manager.tasks[h.taskID] = task
	return nil
}

// IsStreamingRequest implements the TaskHandle interface.
// It determines if this task was initiated via a streaming request.
func (h *mockTaskHandle) IsStreamingRequest() bool {
	// In the mock implementation, we'll check for subscribers as a proxy
	// for determining if this is a streaming request
	for _, sub := range h.manager.tasks {
		if sub.ID == h.taskID {
			// For testing purposes, assume it's streaming if the task exists
			// This is a simplification for the mock
			return true
		}
	}
	return false
}

// GetContextID implements the TaskHandle interface.
func (h *mockTaskHandle) GetContextID() *string {
	task, err := h.manager.Task(h.taskID)
	if err != nil {
		return nil
	}
	return &task.ContextID
}

// AddResponse adds a response to a task.
func (h *mockTaskHandle) AddResponse(response protocol.Message) error {
	task, err := h.manager.Task(h.taskID)
	if err != nil {
		return err
	}

	if task.History == nil {
		task.History = []protocol.Message{}
	}
	task.History = append(task.History, response)
	h.manager.tasks[h.taskID] = task
	return nil
}

// echoProcessor is a simple task processor that echoes messages.
type echoProcessor struct{}

var _ taskmanager.MessageProcessor = (*echoProcessor)(nil)

// ProcessMessage simply echoes the received message.
func (p *echoProcessor) ProcessMessage(
	ctx context.Context, msg protocol.Message, opts taskmanager.ProcessOptions, handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	text := msg.Parts[0].TextContent()
	if text == "" {
		return nil, fmt.Errorf("expected text content, got empty")
	}

	response := protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []*protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Echo: %s", text)),
		},
	}

	return &taskmanager.MessageProcessingResult{
		Result: protocol.NewSendMessageResponseMessage(&response),
	}, nil
}
