// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Helper functions for testing
func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

// setupTestServer creates a test server with the given task manager and options.
// Returns the test server and the A2A server for use in tests.
func setupTestServer(t *testing.T, tm taskmanager.TaskManager, opts ...Option) (*httptest.Server, *A2AServer) {
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(agentCard, tm, opts...)
	require.NoError(t, err)
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
	t.Cleanup(func() {
		testServer.Close()
	})
	return testServer, a2aServer
}

// createJSONRPCRequest creates a JSON-RPC request with the given method and params.
// Returns the request and raw bytes.
func createJSONRPCRequest(t *testing.T, method string, params interface{}, id string) (*http.Request, []byte) {
	paramsBytes, err := json.Marshal(params)
	require.NoError(t, err)

	reqBody := jsonrpc.Request{
		Message: jsonrpc.Message{JSONRPC: "2.0", ID: id},
		Method:  method,
		Params:  json.RawMessage(paramsBytes),
	}
	reqBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "http://test", bytes.NewReader(reqBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	return req, reqBytes
}

// executeRequest executes a HTTP request and returns the response.
func executeRequest(t *testing.T, ts *httptest.Server, req *http.Request, targetURL string) *http.Response {
	// Update URL to point to test server
	req.URL, _ = url.Parse(targetURL)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}

// decodeJSONRPCResponse decodes a JSON-RPC response from an HTTP response.
func decodeJSONRPCResponse(t *testing.T, resp *http.Response) jsonrpc.Response {
	var jsonResp jsonrpc.Response
	err := json.NewDecoder(resp.Body).Decode(&jsonResp)
	require.NoError(t, err)
	return jsonResp
}

// testJSONRPCErrorResponse is a helper that creates and sends a test JSON-RPC request
// and verifies the error response with the expected error code and message pattern.
func testJSONRPCErrorResponse(t *testing.T, server *httptest.Server, method string, reqBody io.Reader,
	contentType string, expectedCode int, expectedMsgPattern string) {

	t.Helper()

	// Create and send request
	req, err := http.NewRequest(method, server.URL, reqBody)
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Parse response
	var jsonResp jsonrpc.Response
	err = json.NewDecoder(resp.Body).Decode(&jsonResp)
	require.NoError(t, err)

	// Verify error
	require.NotNil(t, jsonResp.Error, "Response should contain an error")
	assert.Equal(t, expectedCode, jsonResp.Error.Code, "Error code should match expected")

	if expectedMsgPattern != "" {
		if jsonResp.Error.Message != expectedMsgPattern {
			// Check if it's a detail in the Data field
			if dataStr, ok := jsonResp.Error.Data.(string); ok {
				assert.Contains(t, dataStr, expectedMsgPattern,
					"Error data should contain expected message pattern")
			} else {
				assert.Equal(t, expectedMsgPattern, jsonResp.Error.Message,
					"Error message should match expected")
			}
		}
	}
}

// verifyPushNotificationConfig verifies a push notification configuration response
func verifyPushNotificationConfig(t *testing.T, resp *jsonrpc.Response, expectedID, expectedURL string) {
	t.Helper()

	// Verify response basics
	assert.Nil(t, resp.Error, "Should have no error")
	require.NotNil(t, resp.Result, "Should have a result")

	// Extract and verify the config
	resultBytes, err := json.Marshal(resp.Result)
	require.NoError(t, err)

	var config protocol.TaskPushNotificationConfig
	err = json.Unmarshal(resultBytes, &config)
	require.NoError(t, err)

	assert.Equal(t, expectedID, config.ID)
	assert.Equal(t, expectedURL, config.PushNotificationConfig.URL)
}

// TestA2AServer_HandlerErrors tests various error conditions in the JSON-RPC handler
func TestA2AServer_HandlerErrors(t *testing.T) {
	mockTM := newMockTaskManager()
	testServer, _ := setupTestServer(t, mockTM)

	// Test wrong HTTP method
	t.Run("Wrong HTTP Method", func(t *testing.T) {
		testJSONRPCErrorResponse(t, testServer, http.MethodGet, nil, "",
			jsonrpc.CodeMethodNotFound, "Method not found")
	})

	// Test wrong content type
	t.Run("Wrong Content Type", func(t *testing.T) {
		reqBody := bytes.NewBufferString(
			`{"jsonrpc":"2.0","method":"tasks/send","params":{},"id":"test-id"}`)
		testJSONRPCErrorResponse(t, testServer, http.MethodPost, reqBody, "text/plain",
			jsonrpc.CodeInvalidRequest, "Content-Type")
	})

	// Test invalid JSON body
	t.Run("Invalid JSON Body", func(t *testing.T) {
		reqBody := bytes.NewBufferString(`{invalid-json`)
		testJSONRPCErrorResponse(t, testServer, http.MethodPost, reqBody, "application/json",
			jsonrpc.CodeParseError, "")
	})

	// Test wrong JSONRPC version
	t.Run("Wrong JSONRPC Version", func(t *testing.T) {
		reqBody := bytes.NewBufferString(
			`{"jsonrpc":"1.0","method":"tasks/send","params":{},"id":"test-id"}`)
		testJSONRPCErrorResponse(t, testServer, http.MethodPost, reqBody, "application/json",
			jsonrpc.CodeInvalidRequest, "jsonrpc field must be '2.0'")
	})

	// Test unknown method
	t.Run("Unknown Method", func(t *testing.T) {
		reqBody := bytes.NewBufferString(
			`{"jsonrpc":"2.0","method":"unknown/method","params":{},"id":"test-id"}`)
		testJSONRPCErrorResponse(t, testServer, http.MethodPost, reqBody, "application/json",
			jsonrpc.CodeMethodNotFound, "unknown/method")
	})

	// Test invalid parameters
	t.Run("Invalid Parameters", func(t *testing.T) {
		// Missing required fields in params (Message with no parts)
		reqBody := bytes.NewBufferString(
			`{"jsonrpc":"2.0","method":"tasks/send",
			"params":{"id":"test-task","message":{"role":"user","parts":[]}},"id":"test-id"}`)
		testJSONRPCErrorResponse(t, testServer, http.MethodPost, reqBody, "application/json",
			jsonrpc.CodeInvalidParams, "")
	})
}

// TestA2AServer_AuthMiddleware tests that the authentication middleware works correctly
func TestA2AServer_AuthMiddleware(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()

	// Create API key provider for testing
	keyMap := map[string]string{
		"test-api-key": "test-user",
	}
	authProvider := auth.NewAPIKeyAuthProvider(keyMap, "X-API-Key")

	// Create server with authentication
	a2aServer, err := NewA2AServer(agentCard, mockTM, WithAuthProvider(authProvider))
	require.NoError(t, err)

	// Create test server with the full handler
	testServer := httptest.NewServer(a2aServer.Handler())
	defer testServer.Close()

	t.Run("Auth Success", func(t *testing.T) {
		// Configure mock task manager to succeed
		mockTM.GetResponse = &protocol.Task{
			ID:     "test-task-auth",
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		}
		mockTM.GetError = nil

		// Create valid request with auth header
		req, _ := createJSONRPCRequest(t, protocol.MethodTasksGet,
			protocol.TaskQueryParams{ID: "test-task-auth"}, "req-auth-1")
		req.Header.Set("X-API-Key", "test-api-key") // Valid API key

		resp := executeRequest(t, testServer, req, testServer.URL+"/")
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		jsonResp := decodeJSONRPCResponse(t, resp)
		assert.Nil(t, jsonResp.Error, "Should have no error with valid auth")
		require.NotNil(t, jsonResp.Result, "Should have a result with valid auth")
	})

	t.Run("Auth Failure", func(t *testing.T) {
		// Create request with invalid auth
		req, _ := createJSONRPCRequest(t, protocol.MethodTasksGet,
			protocol.TaskQueryParams{ID: "test-task-auth"}, "req-auth-2")
		req.Header.Set("X-API-Key", "invalid-key") // Invalid API key

		resp := executeRequest(t, testServer, req, testServer.URL+"/")
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Should get 401 with invalid auth")
	})

	// Test that agent card endpoint is still accessible without auth
	t.Run("AgentCard_NoAuth", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, testServer.URL+protocol.AgentCardPath, nil)
		require.NoError(t, err)

		resp, err := testServer.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode, "Agent card should be accessible without auth")
	})
}

// TestA2AServer_PushNotifications tests the push notification endpoints
func TestA2AServer_PushNotifications(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()

	// Create server with JWKS enabled - fix function name
	a2aServer, err := NewA2AServer(agentCard, mockTM, WithJWKSEndpoint(true, ""))
	require.NoError(t, err)

	// Create test server with the full handler
	testServer := httptest.NewServer(a2aServer.Handler())
	defer testServer.Close()

	// Test JWKS endpoint
	t.Run("JWKS_Endpoint", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, testServer.URL+protocol.JWKSPath, nil)
		require.NoError(t, err)

		resp, err := testServer.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		// Verify response structure
		var jwksResponse map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&jwksResponse)
		require.NoError(t, err)

		keys, ok := jwksResponse["keys"].([]interface{})
		require.True(t, ok, "JWKS response should have 'keys' array")
		require.NotEmpty(t, keys, "JWKS should contain at least one key")
	})

	// Test push notification set endpoint
	t.Run("PushNotification_Set", func(t *testing.T) {
		// Configure mock task manager
		mockTM.pushNotificationSetResponse = &protocol.TaskPushNotificationConfig{
			ID: "test-push-task",
			PushNotificationConfig: protocol.PushNotificationConfig{
				URL: "https://example.com/webhook",
			},
		}
		mockTM.pushNotificationSetError = nil

		// Create request
		params := protocol.TaskPushNotificationConfig{
			ID: "test-push-task",
			PushNotificationConfig: protocol.PushNotificationConfig{
				URL: "https://example.com/webhook",
			},
		}

		resp := performJSONRPCRequest(
			t,
			testServer,
			protocol.MethodTasksPushNotificationSet,
			params,
			"req-push-set-1",
		)

		// Use the helper function to verify the response
		verifyPushNotificationConfig(t, resp, "test-push-task", "https://example.com/webhook")
	})

	// Test push notification get endpoint
	t.Run("PushNotification_Get", func(t *testing.T) {
		// Configure mock task manager
		mockTM.pushNotificationGetResponse = &protocol.TaskPushNotificationConfig{
			ID: "test-push-task",
			PushNotificationConfig: protocol.PushNotificationConfig{
				URL: "https://example.com/webhook",
			},
		}
		mockTM.pushNotificationGetError = nil

		// Create request
		params := protocol.TaskIDParams{
			ID: "test-push-task",
		}

		resp := performJSONRPCRequest(
			t,
			testServer,
			protocol.MethodTasksPushNotificationGet,
			params,
			"req-push-get-1",
		)

		// Use the helper function to verify the response
		verifyPushNotificationConfig(t, resp, "test-push-task", "https://example.com/webhook")
	})

	// Test push notification error cases
	t.Run("PushNotification_Errors", func(t *testing.T) {
		// Test push notification get with error
		mockTM.pushNotificationGetError = fmt.Errorf("push notification not found")

		params := protocol.TaskIDParams{
			ID: "nonexistent-task",
		}

		resp := performJSONRPCRequest(
			t,
			testServer,
			protocol.MethodTasksPushNotificationGet,
			params,
			"req-push-get-err",
		)

		assert.NotNil(t, resp.Error, "Should have an error")
		assert.Nil(t, resp.Result, "Should not have a result")
		assert.Equal(t, jsonrpc.CodeInternalError, resp.Error.Code)
	})
}

// TestA2AServer_Resubscribe tests the resubscribe endpoint
func TestA2AServer_Resubscribe(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(agentCard, mockTM)
	require.NoError(t, err)

	// Create test server
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
	defer testServer.Close()

	t.Run("Resubscribe_Success", func(t *testing.T) {
		// Configure mock events
		workingEvent := protocol.TaskStatusUpdateEvent{
			ID:     "resubscribe-task",
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
			Final:  false,
		}
		completedEvent := protocol.TaskStatusUpdateEvent{
			ID:     "resubscribe-task",
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
			Final:  true,
		}
		mockTM.SubscribeEvents = []protocol.TaskEvent{workingEvent, completedEvent}
		mockTM.SubscribeError = nil

		// Add task to mock task manager to ensure it exists
		mockTM.tasks["resubscribe-task"] = &protocol.Task{
			ID: "resubscribe-task",
			Status: protocol.TaskStatus{
				State:     protocol.TaskStateWorking,
				Timestamp: getCurrentTimestamp(),
			},
		}

		// Create request - resubscribe expects SSE response
		params := protocol.TaskIDParams{
			ID: "resubscribe-task",
		}
		paramsBytes, _ := json.Marshal(params)

		reqBody := jsonrpc.Request{
			Message: jsonrpc.Message{JSONRPC: "2.0", ID: "req-resub-1"},
			Method:  protocol.MethodTasksResubscribe,
			Params:  json.RawMessage(paramsBytes),
		}
		reqBytes, _ := json.Marshal(reqBody)

		req, err := http.NewRequest(http.MethodPost, testServer.URL, bytes.NewReader(reqBytes))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream") // Request SSE

		resp, err := testServer.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Verify response headers for SSE
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
		assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))

		// Read and verify events from the SSE stream
		buf := new(bytes.Buffer)
		_, err = buf.ReadFrom(resp.Body)
		require.NoError(t, err)

		sseData := buf.String()
		assert.Contains(t, sseData, "event: task_status_update")
		assert.Contains(t, sseData, `"state":"working"`)
		assert.Contains(t, sseData, `"state":"completed"`)
		assert.Contains(t, sseData, `"final":true`)
	})

	t.Run("Resubscribe_Error", func(t *testing.T) {
		// Configure mock error
		mockTM.SubscribeError = taskmanager.ErrTaskNotFound("nonexistent-task")

		// Create request
		params := protocol.TaskIDParams{
			ID: "nonexistent-task",
		}

		resp := performJSONRPCRequest(
			t,
			testServer,
			protocol.MethodTasksResubscribe,
			params,
			"req-resub-err",
		)

		assert.NotNil(t, resp.Error, "Should have an error")
		assert.Nil(t, resp.Result, "Should not have a result")
		assert.Equal(t, taskmanager.ErrCodeTaskNotFound, resp.Error.Code)
	})
}

// TestA2AServer_StartStop tests the Start and Stop methods
func TestA2AServer_StartStop(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(agentCard, mockTM)
	require.NoError(t, err)

	// Start the server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		// Use a random high port
		errCh <- a2aServer.Start(":0")
	}()

	// Allow server to start
	time.Sleep(100 * time.Millisecond)

	// Stop the server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = a2aServer.Stop(ctx)
	require.NoError(t, err, "Server should stop gracefully")

	// Check if Start() returned an error
	select {
	case err := <-errCh:
		assert.Nil(t, err, "Start should not return an error when stopped properly")
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}
}

// TestA2AServer_HandleTasksSendSubscribe tests the handleTasksSendSubscribe handler
func TestA2AServer_HandleTasksSendSubscribe(t *testing.T) {
	mockTM := newMockTaskManager()
	testServer, _ := setupTestServer(t, mockTM)

	// Configure mock task manager to succeed
	events := make(chan protocol.TaskEvent, 10)
	mockTM.SendTaskSubscribeStream = events
	mockTM.SendTaskSubscribeError = nil

	t.Run("Successful Subscription", func(t *testing.T) {
		// Create SSE event to be sent
		statusUpdate := protocol.TaskStatusUpdateEvent{
			ID:     "test-sub-task",
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
			Final:  false,
		}
		artifactUpdate := protocol.TaskArtifactUpdateEvent{
			ID: "test-sub-task",
			Artifact: protocol.Artifact{
				Name:      stringPtr("test-artifact"),
				LastChunk: boolPtr(true),
				Parts:     []protocol.Part{protocol.NewTextPart("Artifact content")},
			},
			Final: false,
		}
		finalUpdate := protocol.TaskStatusUpdateEvent{
			ID:     "test-sub-task",
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
			Final:  true,
		}

		params := protocol.SendTaskParams{
			ID: "test-sub-task",
			Message: protocol.Message{
				Role:  protocol.MessageRoleUser,
				Parts: []protocol.Part{protocol.NewTextPart("Test message")},
			},
		}

		// Create request - directly using the test server URL instead of "http://test"
		paramsBytes, err := json.Marshal(params)
		require.NoError(t, err)

		reqBody := jsonrpc.Request{
			Message: jsonrpc.Message{JSONRPC: "2.0", ID: "req-1"},
			Method:  protocol.MethodTasksSendSubscribe,
			Params:  json.RawMessage(paramsBytes),
		}
		reqBytes, err := json.Marshal(reqBody)
		require.NoError(t, err)

		req, err := http.NewRequest(http.MethodPost, testServer.URL, bytes.NewReader(reqBytes))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream") // Request SSE

		// We need to close the actual channel right after the test
		// to avoid blocking the test server
		t.Cleanup(func() {
			close(events)
		})

		// Setup a channel to receive the request
		doneCh := make(chan struct{})

		// Launch goroutine to handle the response reading
		go func() {
			resp, err := testServer.Client().Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Verify response is an SSE stream
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

			// Send test events through channel
			events <- statusUpdate
			events <- artifactUpdate
			events <- finalUpdate

			// Read the SSE events
			reader := bufio.NewReader(resp.Body)
			for i := 0; i < 3; i++ {
				// Read the event line, which starts with "event:"
				eventLine, err := reader.ReadString('\n')
				require.NoError(t, err)
				assert.Contains(t, eventLine, "event:")

				// Read the data line, which starts with "data:"
				dataLine, err := reader.ReadString('\n')
				require.NoError(t, err)
				assert.Contains(t, dataLine, "data:")

				// Read the empty line
				_, err = reader.ReadString('\n')
				require.NoError(t, err)
			}

			close(doneCh)
		}()

		// Wait for test to complete or timeout
		select {
		case <-doneCh:
			// Test completed successfully
		case <-time.After(1 * time.Second):
			t.Fatal("Test timed out waiting for SSE response")
		}
	})

	t.Run("Error from Task Manager", func(t *testing.T) {
		// Configure mock task manager to fail
		mockTM.SendTaskSubscribeError = fmt.Errorf("subscription error")

		params := protocol.SendTaskParams{
			ID: "test-sub-error",
			Message: protocol.Message{
				Role:  protocol.MessageRoleUser,
				Parts: []protocol.Part{protocol.NewTextPart("Test message")},
			},
		}

		// Create request
		req, _ := createJSONRPCRequest(t, protocol.MethodTasksSendSubscribe, params, "req-error")

		resp := executeRequest(t, testServer, req, testServer.URL)
		defer resp.Body.Close()

		// Verify response contains error
		jsonResp := decodeJSONRPCResponse(t, resp)
		assert.NotNil(t, jsonResp.Error)
		// Check for error in data field instead of message (internal errors are wrapped)
		if dataStr, ok := jsonResp.Error.Data.(string); ok {
			assert.Contains(t, dataStr, "subscription error")
		} else {
			assert.Fail(t, "Expected error data to contain the error message")
		}
	})

	t.Run("Invalid Parameters", func(t *testing.T) {
		// Create request with invalid params (missing required fields)
		invalidParams := struct {
			ID string `json:"id"`
			// Missing Message field
		}{
			ID: "test-invalid",
		}

		req, _ := createJSONRPCRequest(t, protocol.MethodTasksSendSubscribe, invalidParams, "req-invalid")

		resp := executeRequest(t, testServer, req, testServer.URL)
		defer resp.Body.Close()

		// Verify response contains error
		jsonResp := decodeJSONRPCResponse(t, resp)
		assert.NotNil(t, jsonResp.Error)
		assert.Equal(t, jsonrpc.CodeInvalidParams, jsonResp.Error.Code)
	})
}
