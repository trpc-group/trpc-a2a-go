// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
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
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// Helper to create a default AgentCard for tests.
func defaultAgentCard() AgentCard {
	// Corrected based on types.go definition
	desc := "Agent used for server testing."
	streaming := true
	return AgentCard{
		Name:        "Test Agent",
		Description: desc,
		URL:         "http://localhost:8080/", // Root URL to avoid path extraction
		Version:     "test-agent-v0.1.0",
		Capabilities: AgentCapabilities{
			Streaming: &streaming,
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text", "artifact"},
	}
}

// Helper to perform a JSON-RPC request against the test server.
func performJSONRPCRequest(
	t *testing.T,
	server *httptest.Server,
	method string,
	params interface{},
	requestID interface{},
) *jsonrpc.Response {
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
	// The server normalizes the card to a v1.0-conformant supportedInterfaces
	// list; normalize the expected copy the same way for comparison.
	agentCard.NormalizeInterfaces()
	a2aServer, err := NewA2AServer(mockTM, WithAgentCard(agentCard))
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

func TestA2AServer_initTelemetry_WithProvidedMeterProvider(t *testing.T) {
	provider := noop.NewMeterProvider()
	srv := &A2AServer{telemetryMeterProvider: provider}

	require.NoError(t, srv.initTelemetry(context.Background()))
	require.NotNil(t, srv.telemetryMetrics)
	assert.Equal(t, provider, srv.telemetryMetrics.MeterProvider)
	assert.NotNil(t, srv.telemetryMetrics.RequestCount)
	assert.NotNil(t, srv.telemetryMetrics.OperationDuration)
	assert.NotNil(t, srv.telemetryMetrics.TimeToFirstToken)
	assert.False(t, srv.telemetryOwnsProvider)
}

func TestA2AServer_shutdownTelemetry(t *testing.T) {
	provider := &shutdownAwareMeterProvider{MeterProvider: noop.NewMeterProvider()}
	srv := &A2AServer{
		telemetryMeterProvider: provider,
		telemetryOwnsProvider:  true,
	}

	require.NoError(t, srv.initTelemetry(context.Background()))
	require.NoError(t, srv.shutdownTelemetry(context.Background()))
	assert.True(t, provider.shutdownCalled)
	assert.Nil(t, srv.telemetryMetrics)
	assert.Nil(t, srv.telemetryMeterProvider)
	assert.Nil(t, srv.telemetryShutdown)
	assert.False(t, srv.telemetryOwnsProvider)
}

func TestA2AServer_Start_ShutdownTelemetryOnListenError(t *testing.T) {
	provider := &shutdownAwareMeterProvider{MeterProvider: noop.NewMeterProvider()}
	srv, err := NewA2AServer(newMockTaskManager(), WithAgentCard(defaultAgentCard()))
	require.NoError(t, err)
	srv.telemetryMeterProvider = provider
	srv.telemetryOwnsProvider = true

	err = srv.Start("127.0.0.1:-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ListenAndServe")
	assert.True(t, provider.shutdownCalled)
	assert.Nil(t, srv.telemetryMetrics)
	assert.Nil(t, srv.telemetryMeterProvider)
	assert.Nil(t, srv.telemetryShutdown)
	assert.False(t, srv.telemetryOwnsProvider)
}

func TestA2AServer_initTelemetry_MultipleServersRemainIsolated(t *testing.T) {
	provider1 := noop.NewMeterProvider()
	provider2 := noop.NewMeterProvider()

	srv1 := &A2AServer{telemetryMeterProvider: provider1}
	srv2 := &A2AServer{telemetryMeterProvider: provider2}

	require.NoError(t, srv1.initTelemetry(context.Background()))
	require.NoError(t, srv2.initTelemetry(context.Background()))

	require.NotNil(t, srv1.telemetryMetrics)
	require.NotNil(t, srv2.telemetryMetrics)
	assert.Equal(t, provider1, srv1.telemetryMetrics.MeterProvider)
	assert.Equal(t, provider2, srv2.telemetryMetrics.MeterProvider)
	assert.NotSame(t, srv1.telemetryMetrics, srv2.telemetryMetrics)
}

func TestA2AServer_InitTelemetry_WithInjectedProvider(t *testing.T) {
	srv := &A2AServer{}
	provider := noop.NewMeterProvider()

	srv.SetTelemetryMeterProvider(provider)

	require.NoError(t, srv.InitTelemetry(context.Background()))
	require.NotNil(t, srv.telemetryMetrics)
	assert.Equal(t, provider, srv.telemetryMeterProvider)
	assert.Equal(t, provider, srv.telemetryMetrics.MeterProvider)
	assert.False(t, srv.telemetryOwnsProvider)

	firstMetrics := srv.telemetryMetrics
	require.NoError(t, srv.InitTelemetry(context.Background()))
	assert.Same(t, firstMetrics, srv.telemetryMetrics)
}

func TestA2AServer_HandleJSONRPC_Methods(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(mockTM, WithAgentCard(agentCard))
	require.NoError(t, err)
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
	defer testServer.Close()

	taskID := "test-task-rpc-1"

	// --- Test message/send ---
	t.Run("message/send success", func(t *testing.T) {
		mockTM.sendMessageResponse = &protocol.SendMessageResponse{
			Result: &protocol.Message{
				MessageID: "msg-1",
				Role:      protocol.MessageRoleUser,
				Parts:     []*protocol.Part{protocol.NewTextPart("Response message")},
			},
		}
		mockTM.sendMessageError = nil

		params := protocol.SendMessageParams{
			Message: protocol.Message{
				MessageID: "msg-1",
				Role:      protocol.MessageRoleUser,
				Parts:     []*protocol.Part{protocol.NewTextPart("Input data")},
			},
		}
		resp := performJSONRPCRequest(t, testServer, "SendMessage", params, "req-msg-send")

		assert.Nil(t, resp.Error, "Response error should be nil")
		require.NotNil(t, resp.Result, "Response result should not be nil")

		// Remarshal result interface{} to bytes
		resultBytes, err := json.Marshal(resp.Result)
		require.NoError(t, err, "Failed to remarshal result for MessageResult unmarshalling")
		var resultMsg protocol.SendMessageResponse
		err = json.Unmarshal(resultBytes, &resultMsg)
		require.NoError(t, err, "Failed to unmarshal MessageResult from remarshalled result")
		assert.True(t, resultMsg.GetMessage() != nil || resultMsg.GetTask() != nil)
	})

	t.Run("message/send error", func(t *testing.T) {
		mockTM.sendMessageResponse = nil
		mockTM.sendMessageError = fmt.Errorf("mock send message failed")

		params := protocol.SendMessageParams{
			Message: protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart("Input data")}),
		}
		resp := performJSONRPCRequest(t, testServer, "SendMessage", params, "req-msg-send-fail")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, jsonrpc.CodeInternalError, resp.Error.Code)
		assert.Contains(t, resp.Error.Data, "mock send message failed")
	})

	t.Run("message/send rejects empty role", func(t *testing.T) {
		params := protocol.SendMessageParams{
			Message: protocol.Message{
				MessageID: "msg-no-role",
				Parts:     []*protocol.Part{protocol.NewTextPart("Input data")},
			},
		}
		resp := performJSONRPCRequest(t, testServer, "SendMessage", params, "req-msg-send-no-role")
		assert.Nil(t, resp.Result)
		require.NotNil(t, resp.Error)
		assert.Equal(t, jsonrpc.CodeInvalidParams, resp.Error.Code)
		assert.Contains(t, fmt.Sprint(resp.Error.Data), "role")
	})

	t.Run("message/send rejects non-user role", func(t *testing.T) {
		for _, role := range []protocol.MessageRole{
			protocol.MessageRoleUnspecified,
			protocol.MessageRoleAgent,
			protocol.MessageRole("ROLE_UNKNOWN"),
		} {
			t.Run(string(role), func(t *testing.T) {
				params := protocol.SendMessageParams{
					Message: protocol.Message{
						MessageID: "msg-invalid-role",
						Role:      role,
						Parts:     []*protocol.Part{protocol.NewTextPart("Input data")},
					},
				}
				resp := performJSONRPCRequest(t, testServer, "SendMessage", params, "req-msg-send-invalid-role")
				assert.Nil(t, resp.Result)
				require.NotNil(t, resp.Error)
				assert.Equal(t, jsonrpc.CodeInvalidParams, resp.Error.Code)
				assert.Contains(t, fmt.Sprint(resp.Error.Data), "ROLE_USER")
			})
		}
	})

	t.Run("message/send rejects empty parts", func(t *testing.T) {
		params := protocol.SendMessageParams{
			Message: protocol.Message{
				MessageID: "msg-no-parts",
				Role:      protocol.MessageRoleUser,
			},
		}
		resp := performJSONRPCRequest(t, testServer, "SendMessage", params, "req-msg-send-no-parts")
		assert.Nil(t, resp.Result)
		require.NotNil(t, resp.Error)
		assert.Equal(t, jsonrpc.CodeInvalidParams, resp.Error.Code)
		assert.Contains(t, fmt.Sprint(resp.Error.Data), "part")
	})

	t.Run("message/send rejects null part", func(t *testing.T) {
		params := protocol.SendMessageParams{
			Message: protocol.Message{
				MessageID: "msg-null-part",
				Role:      protocol.MessageRoleUser,
				Parts:     []*protocol.Part{nil},
			},
		}
		resp := performJSONRPCRequest(t, testServer, "SendMessage", params, "req-msg-send-null-part")
		assert.Nil(t, resp.Result)
		require.NotNil(t, resp.Error)
		assert.Equal(t, jsonrpc.CodeInvalidParams, resp.Error.Code)
		assert.Contains(t, fmt.Sprint(resp.Error.Data), "null")
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
		resp := performJSONRPCRequest(t, testServer, "GetTask", params, "req-get-1")

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
		resp := performJSONRPCRequest(t, testServer, "GetTask", params, "req-get-nf")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, jsonrpc.CodeTaskNotFound, resp.Error.Code)
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
		resp := performJSONRPCRequest(t, testServer, "CancelTask", params, "req-cancel-1")

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
		resp := performJSONRPCRequest(t, testServer, "CancelTask", params, "req-cancel-nf")

		assert.Nil(t, resp.Result, "Response result should be nil")
		require.NotNil(t, resp.Error, "Response error should not be nil")
		assert.Equal(t, jsonrpc.CodeTaskNotFound, resp.Error.Code)
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

func TestA2ASrv_HandleMessageStream_SSE(t *testing.T) {
	mockTM := newMockTaskManager()
	agentCard := defaultAgentCard()
	a2aServer, err := NewA2AServer(mockTM, WithAgentCard(agentCard))
	require.NoError(t, err)
	testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
	defer testServer.Close()

	messageID := "test-message-stream-1"
	initialMsg := protocol.Message{
		MessageID: messageID,
		Role:      protocol.MessageRoleUser,
		Parts:     []*protocol.Part{protocol.NewTextPart("SSE test input")},
	}

	// Configure mock streaming events for message/stream
	// Use TaskStatusUpdateEvent to simulate message streaming progress
	event1 := protocol.TaskStatusUpdateEvent{
		TaskID: messageID,
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	}
	event2 := protocol.TaskArtifactUpdateEvent{
		TaskID: messageID,
		Artifact: protocol.Artifact{
			ArtifactID: "stream-artifact-1",
			Parts:      []*protocol.Part{protocol.NewTextPart("Streaming response")},
		},
	}
	final := true
	event3 := protocol.TaskStatusUpdateEvent{
		TaskID: messageID,
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		Final:  final,
	}
	// Configure mock events for message streaming
	mockTM.sendMessageStreamEvents = []protocol.StreamResponse{
		{Result: &event1},
		{Result: &event2},
		{Result: &event3},
	}
	mockTM.sendMessageStreamError = nil

	// Prepare SSE request for message/stream
	params := protocol.SendMessageParams{Message: initialMsg}
	paramsBytes, _ := json.Marshal(params)
	reqBody := jsonrpc.Request{
		Message: jsonrpc.Message{JSONRPC: "2.0", ID: messageID},
		Method:  "SendStreamingMessage",
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
	receivedEvents := []protocol.StreamResponse{}

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

		if eventType == "close" {
			t.Logf("Received close event: %s", string(data))
			return
		}

		var event protocol.StreamResponse
		if err := json.Unmarshal(eventBytes, &event); err != nil {
			t.Fatalf("Failed to unmarshal StreamingMessageEvent: %v. Data: %s", err, string(eventBytes))
		}

		receivedEvents = append(receivedEvents, event)

		// Check context cancellation (e.g., test timeout)
		if ctx.Err() != nil {
			t.Fatalf("Test context canceled: %v", ctx.Err())
		}
	}
	require.Greater(t, len(receivedEvents), 0, "Should have received at least one event")
	var lastStatusEvent *protocol.TaskStatusUpdateEvent
	for i := len(receivedEvents) - 1; i >= 0; i-- {
		if receivedEvents[i].GetStatusUpdate() != nil {
			lastStatusEvent = receivedEvents[i].GetStatusUpdate()
			break
		}
	}
	require.NotNil(t, lastStatusEvent, "Should have received at least one status update event")
	assert.Equal(t, protocol.TaskStateCompleted, lastStatusEvent.Status.State, "State of last status event should be 'completed'")
}

// getCurrentTimestamp returns the current time in ISO 8601 format
func getCurrentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

var _ taskmanager.TaskManager = (*mockTaskManager)(nil)

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
	SubscribeEvents []protocol.StreamResponse
	SubscribeError  error

	// Push notification fields
	pushNotificationSetResponse *protocol.TaskPushNotificationConfig
	pushNotificationSetError    error
	pushNotificationGetResponse *protocol.TaskPushNotificationConfig
	pushNotificationGetError    error
	pushSupported               bool

	// New message handling fields
	sendMessageResponse     *protocol.SendMessageResponse
	sendMessageError        error
	sendMessageStreamEvents []protocol.StreamResponse
	sendMessageStreamError  error
}

// newMockTaskManager creates a new MockTaskManager for testing.
func newMockTaskManager() *mockTaskManager {
	return &mockTaskManager{
		tasks: make(map[string]*protocol.Task),
	}
}

// OnSendMessage implements the TaskManager interface.
func (m *mockTaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sendMessageError != nil {
		return nil, m.sendMessageError
	}

	if m.sendMessageResponse != nil {
		return m.sendMessageResponse, nil
	}

	// Default behavior: create a simple message response
	msg := request.Message
	return &protocol.SendMessageResponse{
		Result: &msg,
	}, nil
}

// OnSendMessageStream implements the TaskManager interface.
func (m *mockTaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sendMessageStreamError != nil {
		return nil, m.sendMessageStreamError
	}

	// Create a channel and send events
	eventCh := make(chan protocol.StreamResponse, len(m.sendMessageStreamEvents)+1)

	// Send configured events in background
	if len(m.sendMessageStreamEvents) > 0 {
		go func() {
			defer close(eventCh)
			for _, event := range m.sendMessageStreamEvents {
				select {
				case <-ctx.Done():
					return
				case eventCh <- event:
					// Continue sending events
				}
			}
		}()
	} else {
		// Default behavior: send the message back as a streaming event
		msg := request.Message
		go func() {
			defer close(eventCh)
			event := protocol.StreamResponse{
				Result: &msg,
			}
			select {
			case <-ctx.Done():
				return
			case eventCh <- event:
				// Event sent
			}
		}()
	}

	return eventCh, nil
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

	// Default implementation if response not configured: echo the flat params back.
	cp := params
	return &cp, nil
}

// OnPushNotificationGet implements the TaskManager interface for push notifications.
func (m *mockTaskManager) OnPushNotificationGet(
	ctx context.Context, params protocol.GetTaskPushNotificationConfigParams,
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
	return nil, fmt.Errorf("push notification config not found for task %s", params.TaskID)
}

// SupportsPushNotifications implements the TaskManager interface.
func (m *mockTaskManager) SupportsPushNotifications() bool { return m.pushSupported }

// OnListTasks implements the TaskManager interface (v1.0 ListTasks).
func (m *mockTaskManager) OnListTasks(
	ctx context.Context, params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	return &protocol.ListTasksResult{Tasks: []*protocol.Task{}}, nil
}

// OnPushNotificationList implements the TaskManager interface (v1.0 list push configs).
func (m *mockTaskManager) OnPushNotificationList(
	ctx context.Context, params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	return &protocol.ListTaskPushNotificationConfigsResult{
		Configs: []protocol.TaskPushNotificationConfig{},
	}, nil
}

// OnPushNotificationDelete implements the TaskManager interface (v1.0 delete push config).
func (m *mockTaskManager) OnPushNotificationDelete(
	ctx context.Context, params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	return nil
}

// OnResubscribe implements the TaskManager interface for resubscribing to task events.
func (m *mockTaskManager) OnResubscribe(
	ctx context.Context, params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
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
	eventCh := make(chan protocol.StreamResponse, len(m.SubscribeEvents)+1)

	// Send configured events in background
	if len(m.SubscribeEvents) > 0 {
		go func() {
			defer close(eventCh)
			for _, streamEvent := range m.SubscribeEvents {
				select {
				case <-ctx.Done():
					return
				case eventCh <- streamEvent:
					// Continue sending events
				}
			}
		}()
	} else {
		// No events configured, send a default completed status
		go func() {
			defer close(eventCh)
			streamEvent := protocol.StreamResponse{
				Result: &protocol.TaskStatusUpdateEvent{
					TaskID: params.ID,
					Final:  true,
					Status: protocol.TaskStatus{
						State:     protocol.TaskStateCompleted,
						Timestamp: getCurrentTimestamp(),
					},
				},
			}

			select {
			case <-ctx.Done():
				return
			case eventCh <- streamEvent:
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

// mockExecutor is a mock implementation of taskmanager.MessageProcessor
type mockExecutor struct{}

func (m *mockExecutor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	// Simple echo processor for testing
	msg := ec.Message
	out := make(chan protocol.StreamEvent, 1)
	out <- &msg
	close(out)
	return out, nil
}

type shutdownAwareMeterProvider struct {
	metric.MeterProvider
	shutdownCalled bool
}

func (p *shutdownAwareMeterProvider) Shutdown(context.Context) error {
	p.shutdownCalled = true
	return nil
}

func TestServer_WithPushNotificationJWKSHandler(t *testing.T) {
	// Create signer
	signer := pushauth.NewJWTSigner()
	err := signer.GenerateKeyPair()
	require.NoError(t, err)

	// Create a task processor and manager
	processor := &mockExecutor{}
	tm, err := memory.NewTaskManager(processor)
	require.NoError(t, err)

	jwksHandler := http.HandlerFunc(signer.HandleJWKS)

	// Create server with a JWKS handler.
	card := AgentCard{
		Name:    "Test Agent",
		Version: "1.0.0",
	}

	server, err := NewA2AServer(
		tm,
		WithAgentCard(card),
		WithPushNotificationJWKSHandler(jwksHandler),
	)
	require.NoError(t, err)

	// Verify the server has the JWKS handler configured.
	assert.NotNil(t, server.pushJWKSHandler)

	// Test JWKS endpoint by creating a test server
	server.jwksEnabled = true
	server.jwksEndpoint = "/.well-known/jwks.json"

	// Create a test HTTP server using the actual A2A router.
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	// Test that the JWKS endpoint works
	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/.well-known/jwks.json", nil)
	require.NoError(t, err)

	resp, err := testServer.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

// Test for authenticated extended card functionality
func TestA2AServer_HandleAgentGetAuthenticatedExtendedCard(t *testing.T) {
	tests := []struct {
		name                     string
		supportsExtendedCard     *bool
		authenticatedCardHandler func(ctx context.Context, baseCard AgentCard) (AgentCard, error)
		expectedError            bool
		expectedErrorCode        int
	}{
		{
			name:                 "not_configured",
			supportsExtendedCard: nil,
			expectedError:        true,
			expectedErrorCode:    jsonrpc.CodeAuthenticatedExtendedCardNotConfigured,
		},
		{
			name:                 "disabled",
			supportsExtendedCard: func() *bool { b := false; return &b }(),
			expectedError:        true,
			expectedErrorCode:    jsonrpc.CodeAuthenticatedExtendedCardNotConfigured,
		},
		{
			name:                 "enabled_no_handler",
			supportsExtendedCard: func() *bool { b := true; return &b }(),
			expectedError:        false,
		},
		{
			name:                 "enabled_with_handler",
			supportsExtendedCard: func() *bool { b := true; return &b }(),
			authenticatedCardHandler: func(ctx context.Context, baseCard AgentCard) (AgentCard, error) {
				// Modify the card to add additional information
				baseCard.Description = "Extended: " + baseCard.Description
				return baseCard, nil
			},
			expectedError: false,
		},
		{
			name:                 "handler_error",
			supportsExtendedCard: func() *bool { b := true; return &b }(),
			authenticatedCardHandler: func(ctx context.Context, baseCard AgentCard) (AgentCard, error) {
				return AgentCard{}, fmt.Errorf("handler failed")
			},
			expectedError:     true,
			expectedErrorCode: jsonrpc.CodeInternalError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockTM := newMockTaskManager()
			agentCard := defaultAgentCard()
			agentCard.SupportsAuthenticatedExtendedCard = tt.supportsExtendedCard

			a2aServer, err := NewA2AServer(mockTM, WithAgentCard(agentCard))
			require.NoError(t, err)

			if tt.authenticatedCardHandler != nil {
				a2aServer.authenticatedCardHandler = tt.authenticatedCardHandler
			}

			testServer := httptest.NewServer(http.HandlerFunc(a2aServer.handleJSONRPC))
			defer testServer.Close()

			// Perform JSON-RPC request
			resp := performJSONRPCRequest(t, testServer, protocol.MethodAgentAuthenticatedExtendedCard, nil, "req-extended-card")

			if tt.expectedError {
				assert.Nil(t, resp.Result, "Response result should be nil")
				require.NotNil(t, resp.Error, "Response error should not be nil")
				assert.Equal(t, tt.expectedErrorCode, resp.Error.Code)
			} else {
				assert.Nil(t, resp.Error, "Response error should be nil")
				require.NotNil(t, resp.Result, "Response result should not be nil")

				// Unmarshal and verify the result
				resultBytes, err := json.Marshal(resp.Result)
				require.NoError(t, err)
				var resultCard AgentCard
				err = json.Unmarshal(resultBytes, &resultCard)
				require.NoError(t, err)

				if tt.authenticatedCardHandler != nil {
					// Verify the handler was called and modified the card
					assert.True(t, strings.HasPrefix(resultCard.Description, "Extended: "))
				} else {
					// Verify the original card was returned
					assert.Equal(t, agentCard.Name, resultCard.Name)
					assert.Equal(t, agentCard.Description, resultCard.Description)
				}
			}
		})
	}
}

// blockMiddleware rejects every request with 401, used to prove the compat
// path is covered by the middleware chain.
type blockMiddleware struct{}

func (blockMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("blocked"))
	})
}

// TestCompatHandler_CoveredByMiddleware verifies that a compat handler
// installed via WithCompatHandler is dispatched INSIDE the auth middleware, so
// legacy (slash) methods cannot bypass authentication. Regression for the
// NewDualHandler auth-bypass finding.
func TestCompatHandler_CoveredByMiddleware(t *testing.T) {
	mockTM := newMockTaskManager()

	compatHit := false
	compat := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compatHit = true
		w.WriteHeader(http.StatusOK)
	})

	srv, err := NewA2AServer(mockTM, WithAgentCard(defaultAgentCard()),
		WithMiddleware(blockMiddleware{}),
		WithCompatHandler(compat),
	)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Legacy slash method must be blocked by the middleware, NOT reach compat.
	resp, err := http.Post(ts.URL, "application/json",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":"1","method":"message/send","params":{}}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "legacy method must be authenticated")
	assert.False(t, compatHit, "compat handler must not run before auth middleware")

	// v1 method is likewise blocked.
	resp2, err := http.Post(ts.URL, "application/json",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":"2","method":"SendMessage","params":{}}`)))
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// TestServer_ServingPathsIndependentOfSupportedInterfaces verifies mount paths
// stay at the root defaults even when SupportedInterfaces advertise a subpath.
// Card URLs are client discovery metadata; use WithBasePath to listen elsewhere.
func TestServer_ServingPathsIndependentOfSupportedInterfaces(t *testing.T) {
	card := AgentCard{
		Name:        "Test Agent",
		Description: "x",
		Version:     "v1",
		SupportedInterfaces: []protocol.AgentInterface{
			{URL: "http://localhost:8080/agent/api", ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0"},
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}
	srv, err := NewA2AServer(newMockTaskManager(), WithAgentCard(card))
	require.NoError(t, err)
	assert.Equal(t, "/", srv.jsonRPCEndpoint)
	assert.Equal(t, protocol.AgentCardPath, srv.agentCardPath)
	assert.False(t, srv.httpJSONEnabled)
}

func TestServerRejectsPathsServeMuxWouldClean(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	for _, requestPath := range []string{
		"/./tasks/task-1",
		"/%2e/tasks/task-1",
		"/tenant/../tasks/task-1",
		"//tasks/task-1",
	} {
		t.Run(requestPath, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, requestPath, nil)
			srv.Handler().ServeHTTP(recorder, req)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Empty(t, recorder.Header().Get("Location"), "must not redirect to a different A2A operation")
		})
	}
}

func TestHTTPJSONTenantCanMatchJSONRPCEndpointPrefix(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithJSONRPCEndpoint("/rpc/"),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	// The REST tenant "rpc" must not be swallowed by the /rpc/ JSON-RPC pattern.
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rpc/tasks/missing-task", nil)
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, protocol.MediaTypeA2AJSON, recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), "TASK_NOT_FOUND")

	// The exact configured endpoint remains JSON-RPC.
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rpc/", bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"missing-task"}}`,
	)))
	req.Header.Set("Content-Type", protocol.MediaTypeJSON)
	srv.Handler().ServeHTTP(recorder, req)
	assert.Contains(t, recorder.Body.String(), `"jsonrpc":"2.0"`)

	// Compare escaped path forms consistently when an explicitly configured
	// JSON-RPC endpoint contains a character that must be percent-encoded.
	escapedSrv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithJSONRPCEndpoint("/rpc endpoint/"),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rpc%20endpoint/", bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"missing-task"}}`,
	)))
	req.Header.Set("Content-Type", protocol.MediaTypeJSON)
	escapedSrv.Handler().ServeHTTP(recorder, req)
	assert.Contains(t, recorder.Body.String(), `"jsonrpc":"2.0"`)
}

func TestV1JSONRPCBindingCanBeDisabled(t *testing.T) {
	_, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithV1JSONRPCEnabled(false),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enabled protocol binding")

	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithV1JSONRPCEnabled(false),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"missing-task"}}`,
	)))
	req.Header.Set("Content-Type", protocol.MediaTypeJSON)
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), `"jsonrpc"`)

	compatHit := false
	compat := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		compatHit = true
		w.WriteHeader(http.StatusNoContent)
	})
	srv, err = NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithV1JSONRPCEnabled(false),
		WithCompatHandler(compat),
	)
	require.NoError(t, err)
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":"1","method":"message/send","params":{}}`,
	)))
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, compatHit)
}

func TestHandleSSEStreamSkipsUnknownEvents(t *testing.T) {
	events := make(chan protocol.StreamResponse, 2)
	events <- protocol.StreamResponse{}
	events <- protocol.StreamResponse{Result: &protocol.Message{MessageID: "message-1"}}
	close(events)

	formatted := 0
	recorder := httptest.NewRecorder()
	handleSSEStream(
		context.Background(),
		false,
		recorder,
		recorder,
		events,
		"request-1",
		nil,
		func(_ io.Writer, batch []sse.EventBatch) error {
			formatted += len(batch)
			return nil
		},
	)
	assert.Equal(t, 1, formatted)
}

func TestHandleSSEStreamDrainsAfterFormatError(t *testing.T) {
	events := make(chan protocol.StreamResponse)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(events)
		events <- protocol.StreamResponse{Result: &protocol.Message{MessageID: "message-1"}}
		events <- protocol.StreamResponse{Result: &protocol.Message{MessageID: "message-2"}}
	}()

	recorder := httptest.NewRecorder()
	handleSSEStream(
		context.Background(),
		false,
		recorder,
		recorder,
		events,
		"request-1",
		nil,
		func(io.Writer, []sse.EventBatch) error { return fmt.Errorf("write failed") },
	)

	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("event producer remained blocked after SSE format error")
	}
}

func TestHandleSSEStreamDrainsAfterClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := make(chan protocol.StreamResponse)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(events)
		events <- protocol.StreamResponse{Result: &protocol.Message{MessageID: "message-1"}}
	}()

	recorder := httptest.NewRecorder()
	handleSSEStream(ctx, false, recorder, recorder, events, "request-1", nil, sse.FormatEventBatch)

	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("event producer remained blocked after SSE client disconnect")
	}
}
