// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Helper to create a default AgentCard for tests.
func defaultAgentCard() AgentCard {
	// Corrected based on types.go definition
	desc := "Agent used for server testing."
	return AgentCard{
		Name:        "Test Agent",
		Description: &desc,
		URL:         "http://localhost/test-agent", // Example URL
		Version:     "test-agent-v0.1.0",
		Capabilities: AgentCapabilities{
			Streaming: true,
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text", "artifact"},
	}
}

// Helper function to get a pointer to a string (for optional fields)
func stringPtr(s string) *string {
	return &s
}

// Helper function to get a pointer to a boolean.
func boolPtr(b bool) *bool {
	return &b
}

// Helper to perform a JSON-RPC request against the test server.
func performJSONRPCRequest(t *testing.T, server *httptest.Server, method string, params interface{}, requestID interface{}) *jsonrpc.Response {
	t.Helper()

	// Marshal params
	paramsBytes, err := json.Marshal(params)
	require.NoError(t, err, "Failed to marshal params for request")

	// Create request body
	reqBody := jsonrpc.Request{
		Message: jsonrpc.Message{JSONRPC: "2.0", ID: requestID},
		Method:  method,
		Params:  json.RawMessage(paramsBytes),
	}
	reqBytes, err := json.Marshal(reqBody)
	require.NoError(t, err, "Failed to marshal request body")

	// Perform HTTP POST
	httpReq, err := http.NewRequest(http.MethodPost, server.URL+"/", bytes.NewReader(reqBytes))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := server.Client().Do(httpReq)
	require.NoError(t, err, "HTTP request failed")
	defer resp.Body.Close()

	// Read and unmarshal response body
	respBodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "Failed to read response body")

	var jsonResp jsonrpc.Response
	err = json.Unmarshal(respBodyBytes, &jsonResp)
	require.NoError(t, err, "Failed to unmarshal JSON-RPC response. Body: %s", string(respBodyBytes))

	return &jsonResp
}

func TestA2AServer_HandleAgentCard(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(agentCard, mockTM)
	require.NoError(t, err)
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleAgentCard))
	defer testServer.Close()

	req, err := http.NewRequest(http.MethodGet, testServer.URL, nil)
	require.NoError(t, err)

	resp, err := testServer.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Status code should be OK")
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"), "Content-Type should be application/json")
	// Check CORS header (enabled by default)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))

	// Decode and compare body
	var receivedCard AgentCard
	err = json.NewDecoder(resp.Body).Decode(&receivedCard)
	require.NoError(t, err, "Failed to decode agent card from response")
	assert.Equal(t, agentCard, receivedCard, "Received agent card should match original")
}

func TestA2AServer_HandleJSONRPC_Methods(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(agentCard, mockTM)
	require.NoError(t, err)
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
	defer testServer.Close()

	taskID := "test-task-rpc-1"
	initialMsg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Input data")}}

	// --- Test tasks/send ---
	t.Run("tasks/send success", func(t *testing.T) {
		mockTM.SendResponse = &protocol.Task{
			ID:     taskID,
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
		}
		mockTM.SendError = nil

		params := protocol.SendTaskParams{ID: taskID, Message: initialMsg}
		resp := performJSONRPCRequest(t, testServer, "tasks/send", params, taskID)

		assert.Nil(t, resp.Error, "Response error should be nil")
		require.NotNil(t, resp.Result, "Response result should not be nil")

		// Remarshal result interface{} to bytes
		resultBytes, err := json.Marshal(resp.Result)
		require.NoError(t, err, "Failed to remarshal result for Task unmarshalling")
		var resultTask protocol.Task
		err = json.Unmarshal(resultBytes, &resultTask)
		require.NoError(t, err, "Failed to unmarshal task from remarshalled result")
		assert.Equal(t, taskID, resultTask.ID)
		assert.Equal(t, protocol.TaskStateWorking, resultTask.Status.State)
	})

	t.Run("tasks/send error", func(t *testing.T) {
		mockTM.SendResponse = nil
		mockTM.SendError = fmt.Errorf("mock send task failed")

		params := protocol.SendTaskParams{ID: "task-send-fail", Message: initialMsg}
		resp := performJSONRPCRequest(t, testServer, "tasks/send", params, "req-send-fail")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, jsonrpc.CodeInternalError, resp.Error.Code)
		assert.Contains(t, resp.Error.Data, "mock send task failed")
	})

	// --- Test tasks/get ---
	t.Run("tasks/get success", func(t *testing.T) {
		mockTM.GetResponse = &protocol.Task{
			ID:     taskID,
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		}
		mockTM.GetError = nil
		mockTM.tasks[taskID] = mockTM.GetResponse // Ensure task exists in mock

		params := protocol.TaskQueryParams{ID: taskID}
		resp := performJSONRPCRequest(t, testServer, "tasks/get", params, "req-get-1")

		assert.Nil(t, resp.Error, "Response error should be nil")
		require.NotNil(t, resp.Result, "Response result should not be nil")

		// Remarshal result interface{} to bytes
		resultBytes, err := json.Marshal(resp.Result)
		require.NoError(t, err, "Failed to remarshal result for Task unmarshalling")
		var resultTask protocol.Task
		err = json.Unmarshal(resultBytes, &resultTask)
		require.NoError(t, err, "Failed to unmarshal task from remarshalled result")
		assert.Equal(t, taskID, resultTask.ID)
		assert.Equal(t, protocol.TaskStateCompleted, resultTask.Status.State)
	})

	t.Run("tasks/get not found", func(t *testing.T) {
		mockTM.GetError = taskmanager.ErrTaskNotFound("task-not-found")

		params := protocol.TaskQueryParams{ID: "task-not-found"}
		resp := performJSONRPCRequest(t, testServer, "tasks/get", params, "req-get-nf")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, taskmanager.ErrCodeTaskNotFound, resp.Error.Code)
	})

	// --- Test tasks/cancel ---
	t.Run("tasks/cancel success", func(t *testing.T) {
		mockTM.CancelResponse = &protocol.Task{
			ID:     taskID,
			Status: protocol.TaskStatus{State: protocol.TaskStateCanceled},
		}
		mockTM.CancelError = nil
		// Ensure task exists in mock (e.g., from previous send test)
		mockTM.tasks[taskID] = &protocol.Task{ID: taskID, Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}

		params := protocol.TaskIDParams{ID: taskID}
		resp := performJSONRPCRequest(t, testServer, "tasks/cancel", params, "req-cancel-1")

		assert.Nil(t, resp.Error, "Response error should be nil")
		require.NotNil(t, resp.Result, "Response result should not be nil")

		// Remarshal result interface{} to bytes
		resultBytes, err := json.Marshal(resp.Result)
		require.NoError(t, err, "Failed to remarshal result for Task unmarshalling")
		var resultTask protocol.Task
		err = json.Unmarshal(resultBytes, &resultTask)
		require.NoError(t, err, "Failed to unmarshal task from remarshalled result")
		assert.Equal(t, taskID, resultTask.ID)
		assert.Equal(t, protocol.TaskStateCanceled, resultTask.Status.State)
	})

	t.Run("tasks/cancel not found", func(t *testing.T) {
		mockTM.CancelError = taskmanager.ErrTaskNotFound("task-cancel-nf")

		params := protocol.TaskIDParams{ID: "task-cancel-nf"}
		resp := performJSONRPCRequest(t, testServer, "tasks/cancel", params, "req-cancel-nf")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, taskmanager.ErrCodeTaskNotFound, resp.Error.Code)
	})

	// --- Test unknown method ---
	t.Run("unknown method", func(t *testing.T) {
		params := map[string]string{"data": "foo"}
		resp := performJSONRPCRequest(t, testServer, "tasks/unknown", params, "req-unknown")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, jsonrpc.CodeMethodNotFound, resp.Error.Code)
	})
}

func TestA2ASrv_HandleTasksSendSub_SSE(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(agentCard, mockTM)
	require.NoError(t, err)
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
	defer testServer.Close()

	taskID := "test-task-sse-1"
	initialMsg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("SSE test input")}}

	// Configure mock events
	event1 := protocol.TaskStatusUpdateEvent{
		ID:     taskID,
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	}
	event2 := protocol.TaskArtifactUpdateEvent{
		ID: taskID,
		Artifact: protocol.Artifact{
			Index: 0,
			Parts: []protocol.Part{protocol.NewTextPart("Intermediate result")},
		},
	}
	event3 := protocol.TaskStatusUpdateEvent{
		ID:     taskID,
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		Final:  true,
	}
	mockTM.SubscribeEvents = []protocol.TaskEvent{event1, event2, event3}
	mockTM.SubscribeError = nil

	// Prepare SSE request
	params := protocol.SendTaskParams{ID: taskID, Message: initialMsg}
	paramsBytes, _ := json.Marshal(params)
	reqBody := jsonrpc.Request{
		Message: jsonrpc.Message{JSONRPC: "2.0", ID: taskID},
		Method:  "tasks/sendSubscribe",
		Params:  json.RawMessage(paramsBytes),
	}
	reqBytes, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/", bytes.NewReader(reqBytes))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream") // Critical for SSE

	// Perform request
	resp, err := testServer.Client().Do(httpReq)
	require.NoError(t, err, "HTTP request for SSE failed")
	defer resp.Body.Close()

	// Assert initial response
	require.Equal(t, http.StatusOK, resp.StatusCode, "SSE initial response status should be OK")
	require.True(t, strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"), "Content-Type should be text/event-stream")
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"), "Cache-Control should be no-cache")
	assert.Equal(t, "keep-alive", resp.Header.Get("Connection"), "Connection should be keep-alive")

	// Read and verify SSE events
	reader := sse.NewEventReader(resp.Body) // Use the client's SSE reader
	receivedEvents := []protocol.TaskEvent{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for {
		data, eventType, err := reader.ReadEvent()
		if err == io.EOF {
			break // End of stream
		}
		if err != nil {
			t.Fatalf("Error reading SSE event: %v", err)
		}
		if len(data) == 0 { // Skip keep-alive comments/empty lines
			continue
		}
		var jsonRPCResponse jsonrpc.RawResponse
		if err := json.Unmarshal(data, &jsonRPCResponse); err != nil {
			t.Logf("Not a JSON-RPC response: %s", string(data))
			if eventType == "close" {
				t.Logf("Received close event: %s", string(data))
				break
			}
			continue
		}
		if jsonRPCResponse.Error != nil {
			t.Fatalf("JSON-RPC error in SSE event: %v", jsonRPCResponse.Error)
			continue
		}
		eventBytes := jsonRPCResponse.Result

		var event protocol.TaskEvent
		switch eventType {
		case "task_status_update":
			var statusEvent protocol.TaskStatusUpdateEvent
			if err := json.Unmarshal(eventBytes, &statusEvent); err != nil {
				t.Fatalf("Failed to unmarshal task_status_update: %v. Data: %s", err, string(eventBytes))
			}
			event = statusEvent
		case "task_artifact_update":
			var artifactEvent protocol.TaskArtifactUpdateEvent
			if err := json.Unmarshal(eventBytes, &artifactEvent); err != nil {
				t.Fatalf("Failed to unmarshal task_artifact_update: %v. Data: %s", err, string(eventBytes))
			}
			event = artifactEvent
		case "close": // Handle potential close event
			t.Logf("Received close event: %s", string(data))
			break
		default:
			t.Logf("Skipping unknown event type: %s", eventType)
			continue
		}

		if event != nil {
			receivedEvents = append(receivedEvents, event)
		}

		// Check context cancellation (e.g., test timeout)
		if ctx.Err() != nil {
			t.Fatalf("Test context canceled: %v", ctx.Err())
		}
	}
	require.Greater(t, len(receivedEvents), 0, "Should have received at least one event")
	var lastStatusEvent protocol.TaskStatusUpdateEvent
	for i := len(receivedEvents) - 1; i >= 0; i-- {
		if statusEvent, ok := receivedEvents[i].(protocol.TaskStatusUpdateEvent); ok {
			lastStatusEvent = statusEvent
			break
		}
	}
	require.NotEmpty(t, lastStatusEvent.ID, "Should have received at least one status update event")
	assert.Equal(t, protocol.TaskStateCompleted, lastStatusEvent.Status.State, "State of last status event should be 'completed'")
}

// getCurrentTimestamp returns the current time in ISO 8601 format
func getCurrentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// mockTaskManager implements the taskmanager.TaskManager interface for testing.
type mockTaskManager struct {
	mu sync.Mutex
	// Store tasks for basic Get/Cancel simulation
	tasks map[string]*protocol.Task

	// Configure responses/behavior for testing
	SendResponse    *protocol.Task
	SendError       error
	GetResponse     *protocol.Task
	GetError        error
	CancelResponse  *protocol.Task
	CancelError     error
	SubscribeEvents []protocol.TaskEvent // Events to send for subscription
	SubscribeError  error

	// Push notification fields
	pushNotificationSetResponse *protocol.TaskPushNotificationConfig
	pushNotificationSetError    error
	pushNotificationGetResponse *protocol.TaskPushNotificationConfig
	pushNotificationGetError    error
}

// newMockTaskManager creates a new mockTaskManager for testing.
func newMockTaskManager() *mockTaskManager {
	return &mockTaskManager{
		tasks: make(map[string]*protocol.Task),
	}
}

// OnSendTask implements the TaskManager interface.
func (m *mockTaskManager) OnSendTask(
	ctx context.Context,
	params protocol.SendTaskParams,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return configured error if set
	if m.SendError != nil {
		return nil, m.SendError
	}

	// Validate required fields
	if params.ID == "" {
		return nil, jsonrpc.ErrInvalidParams("task ID is required")
	}

	if len(params.Message.Parts) == 0 {
		return nil, jsonrpc.ErrInvalidParams("message must have at least one part")
	}

	// Return configured response if set
	if m.SendResponse != nil {
		// Store for later retrieval
		m.tasks[m.SendResponse.ID] = m.SendResponse
		return m.SendResponse, nil
	}

	// Default behavior: create a simple task
	task := protocol.NewTask(params.ID, params.SessionID)
	now := getCurrentTimestamp()
	task.Status = protocol.TaskStatus{
		State:     protocol.TaskStateSubmitted,
		Timestamp: now,
	}

	// Store for later retrieval
	m.tasks[task.ID] = task
	return task, nil
}

// OnGetTask implements the TaskManager interface.
func (m *mockTaskManager) OnGetTask(
	ctx context.Context, params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.GetError != nil {
		return nil, m.GetError
	}

	if m.GetResponse != nil {
		return m.GetResponse, nil
	}

	// Check if task exists
	task, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}
	return task, nil
}

// OnCancelTask implements the TaskManager interface.
func (m *mockTaskManager) OnCancelTask(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.CancelError != nil {
		return nil, m.CancelError
	}

	if m.CancelResponse != nil {
		return m.CancelResponse, nil
	}

	// Check if task exists
	task, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// Update task status to canceled
	task.Status.State = protocol.TaskStateCanceled
	task.Status.Timestamp = getCurrentTimestamp()
	return task, nil
}

// OnSendTaskSubscribe implements the TaskManager interface.
func (m *mockTaskManager) OnSendTaskSubscribe(
	ctx context.Context, params protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.SubscribeError != nil {
		return nil, m.SubscribeError
	}

	// Create a task like OnSendTask would
	task := protocol.NewTask(params.ID, params.SessionID)
	task.Status = protocol.TaskStatus{
		State:     protocol.TaskStateSubmitted,
		Timestamp: getCurrentTimestamp(),
	}

	// Store for later retrieval
	m.tasks[task.ID] = task

	// Create a channel and send events
	eventCh := make(chan protocol.TaskEvent, len(m.SubscribeEvents)+1)

	// Send configured events in background
	if len(m.SubscribeEvents) > 0 {
		go func() {
			for _, event := range m.SubscribeEvents {
				select {
				case <-ctx.Done():
					close(eventCh)
					return
				case eventCh <- event:
					// If this is the final event, close the channel
					if event.IsFinal() {
						close(eventCh)
						return
					}
				}
			}
			// If we didn't have a final event, close the channel anyway
			close(eventCh)
		}()
	} else {
		// No events configured, send a default working and completed status
		go func() {
			// Working status
			workingEvent := protocol.TaskStatusUpdateEvent{
				ID: params.ID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateWorking,
					Timestamp: getCurrentTimestamp(),
				},
				Final: false,
			}

			// Completed status
			completedEvent := protocol.TaskStatusUpdateEvent{
				ID: params.ID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateCompleted,
					Timestamp: getCurrentTimestamp(),
				},
				Final: true,
			}

			select {
			case <-ctx.Done():
				close(eventCh)
				return
			case eventCh <- workingEvent:
				// Continue
			}

			select {
			case <-ctx.Done():
				close(eventCh)
				return
			case eventCh <- completedEvent:
				close(eventCh)
				return
			}
		}()
	}

	return eventCh, nil
}

// OnPushNotificationSet implements the TaskManager interface for push notifications.
func (m *mockTaskManager) OnPushNotificationSet(
	ctx context.Context, params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pushNotificationSetError != nil {
		return nil, m.pushNotificationSetError
	}

	if m.pushNotificationSetResponse != nil {
		return m.pushNotificationSetResponse, nil
	}

	// Default implementation if response not configured
	return &protocol.TaskPushNotificationConfig{
		ID:                     params.ID,
		PushNotificationConfig: params.PushNotificationConfig,
	}, nil
}

// OnPushNotificationGet implements the TaskManager interface for push notifications.
func (m *mockTaskManager) OnPushNotificationGet(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pushNotificationGetError != nil {
		return nil, m.pushNotificationGetError
	}

	if m.pushNotificationGetResponse != nil {
		return m.pushNotificationGetResponse, nil
	}

	// Default not found response
	return nil, fmt.Errorf("push notification config not found for task %s", params.ID)
}

// OnResubscribe implements the TaskManager interface for resubscribing to task events.
func (m *mockTaskManager) OnResubscribe(
	ctx context.Context, params protocol.TaskIDParams,
) (<-chan protocol.TaskEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.SubscribeError != nil {
		return nil, m.SubscribeError
	}

	// Check if task exists
	_, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// Create a channel and send events
	eventCh := make(chan protocol.TaskEvent, len(m.SubscribeEvents)+1)

	// Send configured events in background
	if len(m.SubscribeEvents) > 0 {
		go func() {
			for _, event := range m.SubscribeEvents {
				select {
				case <-ctx.Done():
					close(eventCh)
					return
				case eventCh <- event:
					// If this is the final event, close the channel
					if event.IsFinal() {
						close(eventCh)
						return
					}
				}
			}
			// If we didn't have a final event, close the channel anyway
			close(eventCh)
		}()
	} else {
		// No events configured, send a default completed status
		go func() {
			completedEvent := protocol.TaskStatusUpdateEvent{
				ID: params.ID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateCompleted,
					Timestamp: getCurrentTimestamp(),
				},
				Final: true,
			}

			select {
			case <-ctx.Done():
				close(eventCh)
				return
			case eventCh <- completedEvent:
				close(eventCh)
				return
			}
		}()
	}

	return eventCh, nil
}

// ProcessTask is a helper method for tests that need to process a task directly.
func (m *mockTaskManager) ProcessTask(
	ctx context.Context, taskID string, msg protocol.Message,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if task exists
	task, exists := m.tasks[taskID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(taskID)
	}

	// Update task status to working
	task.Status.State = protocol.TaskStateWorking
	task.Status.Timestamp = getCurrentTimestamp()

	// Add message to history if it exists
	if task.History == nil {
		task.History = make([]protocol.Message, 0)
	}
	task.History = append(task.History, msg)

	return task, nil
}
