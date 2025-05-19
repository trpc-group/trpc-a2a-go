// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
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
	_, err = basicClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task1",
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
	task, err := authClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task1",
		Message: createTextMessage("Hello, World!"),
	})
	require.NoError(t, err, "Authenticated request failed")
	assert.Equal(t, "task1", task.ID, "Task ID mismatch")

	// Get the processed task to verify it was processed
	processedTask, err := taskMgr.(*mockTaskManager).Task("task1")
	require.NoError(t, err, "Failed to get task")
	assert.Equal(t, protocol.TaskStateCompleted, processedTask.Status.State, "Task should be done")
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
	_, err = basicClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task2",
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
	task, err := authClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task2",
		Message: createTextMessage("Hello from API key test!"),
	})
	require.NoError(t, err, "Authenticated request failed")
	assert.Equal(t, "task2", task.ID, "Task ID mismatch")

	// Get the processed task to verify it was processed
	processedTask, err := taskMgr.(*mockTaskManager).Task("task2")
	require.NoError(t, err, "Failed to get task")
	assert.Equal(t, protocol.TaskStateCompleted, processedTask.Status.State, "Task should be done")
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
	_, err = basicClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task3",
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

	task, err := jwtClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task3",
		Message: createTextMessage("Hello with JWT auth!"),
	})
	require.NoError(t, err, "JWT authenticated request failed")
	assert.Equal(t, "task3", task.ID, "Task ID mismatch")

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

	task, err = apiKeyClient.SendTasks(ctx, protocol.SendTaskParams{
		ID:      "task4",
		Message: createTextMessage("Hello with API key auth!"),
	})
	require.NoError(t, err, "API key authenticated request failed")
	assert.Equal(t, "task4", task.ID, "Task ID mismatch")
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
			PushNotifications: true,
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
		Role: protocol.MessageRoleUser,
		Parts: []protocol.Part{
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
			Streaming: true,
		},
		Authentication: &protocol.AuthenticationInfo{
			Schemes: []string{"apiKey", "jwt"},
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
	processor   taskmanager.TaskProcessor
	tasks       map[string]*protocol.Task
	pushConfigs map[string]protocol.PushNotificationConfig
}

// newMockTaskManager creates a new mock task manager.
func newMockTaskManager(processor taskmanager.TaskProcessor) *mockTaskManager {
	return &mockTaskManager{
		processor:   processor,
		tasks:       make(map[string]*protocol.Task),
		pushConfigs: make(map[string]protocol.PushNotificationConfig),
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

// OnSendTask handles sending a task.
func (m *mockTaskManager) OnSendTask(
	ctx context.Context, params protocol.SendTaskParams,
) (*protocol.Task, error) {
	task := protocol.NewTask(params.ID, params.SessionID)
	m.tasks[params.ID] = task

	handle := &mockTaskHandle{
		taskID:  params.ID,
		manager: m,
	}

	if err := handle.UpdateStatus(protocol.TaskStateWorking, nil); err != nil {
		return task, err
	}

	if m.processor != nil {
		if err := m.processor.Process(ctx, params.ID, params.Message, handle); err != nil {
			return task, err
		}
	} else {
		// Mark the task as done if no processor
		if err := handle.UpdateStatus(protocol.TaskStateCompleted, nil); err != nil {
			return task, err
		}
	}

	return m.tasks[params.ID], nil
}

// OnGetTask handles getting a task.
func (m *mockTaskManager) OnGetTask(
	ctx context.Context, params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	return m.Task(params.ID)
}

// OnListTasks handles listing tasks.
func (m *mockTaskManager) OnListTasks(
	ctx context.Context, params protocol.TaskQueryParams,
) ([]*protocol.Task, error) {
	var tasks []*protocol.Task
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// OnPushNotificationSet sets a push notification configuration for a task.
func (m *mockTaskManager) OnPushNotificationSet(
	ctx context.Context, params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	_, err := m.Task(params.ID)
	if err != nil {
		return nil, err
	}

	m.pushConfigs[params.ID] = params.PushNotificationConfig
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
		return nil, taskmanager.ErrPushNotificationNotConfigured(params.ID)
	}

	return &protocol.TaskPushNotificationConfig{
		ID:                     params.ID,
		PushNotificationConfig: config,
	}, nil
}

// OnResubscribe handles resubscribing to a task.
func (m *mockTaskManager) OnResubscribe(
	ctx context.Context, params protocol.TaskIDParams,
) (<-chan protocol.TaskEvent, error) {
	task, err := m.Task(params.ID)
	if err != nil {
		return nil, err
	}

	// Create a channel for events
	eventCh := make(chan protocol.TaskEvent)

	// For a mock implementation, just send one event with the current status and close the channel
	go func() {
		// Send the current task status
		event := protocol.TaskStatusUpdateEvent{
			ID:     task.ID,
			Status: task.Status,
			Final:  true,
		}

		// Try to send the event, but don't block forever
		select {
		case eventCh <- event:
			// Event sent successfully
		case <-ctx.Done():
			// Context was canceled
		}

		// Close the channel
		close(eventCh)
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

// OnSubscribeTaskUpdates handles subscribing to task updates.
func (m *mockTaskManager) OnSubscribeTaskUpdates(
	ctx context.Context, params protocol.TaskQueryParams, eventCh chan<- protocol.TaskEvent,
) error {
	return fmt.Errorf("streaming not implemented in mock task manager")
}

// OnSendTaskSubscribe handles sending a task and subscribing to updates.
func (m *mockTaskManager) OnSendTaskSubscribe(
	ctx context.Context, params protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	// Create a channel for events
	eventCh := make(chan protocol.TaskEvent, 10)

	// Create the task
	task, err := m.OnSendTask(ctx, params)
	if err != nil {
		close(eventCh)
		return nil, err
	}

	// For a mock implementation, just send one event with the current status and close the channel
	go func() {
		// Send the current task status
		event := protocol.TaskStatusUpdateEvent{
			ID:     task.ID,
			Status: task.Status,
			Final:  true,
		}

		// Try to send the event, but don't block forever
		select {
		case eventCh <- event:
			// Event sent successfully
		case <-ctx.Done():
			// Context was canceled
		}

		// Close the channel
		close(eventCh)
	}()

	return eventCh, nil
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

// GetSessionID implements the TaskHandle interface.
func (h *mockTaskHandle) GetSessionID() *string {
	task, err := h.manager.Task(h.taskID)
	if err != nil {
		return nil
	}
	return task.SessionID
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

// Process simply echoes the received message.
func (p *echoProcessor) Process(
	ctx context.Context, taskID string, msg protocol.Message, handle taskmanager.TaskHandle,
) error {
	// Create a response that echoes back the message
	textPart, ok := msg.Parts[0].(protocol.TextPart)
	if !ok {
		return fmt.Errorf("expected TextPart, got %T", msg.Parts[0])
	}

	response := protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Echo: %s", textPart.Text)),
		},
	}

	// Mark the task as done with the response
	return handle.UpdateStatus(protocol.TaskStateCompleted, &response)
}
