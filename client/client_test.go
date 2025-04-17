// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// TestA2AClient_SendTask tests the SendTask client method covering success,
// JSON-RPC error responses, and HTTP error scenarios.
func TestA2AClient_SendTask(t *testing.T) {
	taskID := "client-task-1"
	params := protocol.SendTaskParams{
		ID: taskID,
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("Client test input")},
		},
	}
	paramsBytes, err := json.Marshal(params)
	require.NoError(t, err) // Should not fail marshalling test data.

	expectedRequest := &jsonrpc.Request{
		Message: jsonrpc.Message{JSONRPC: "2.0", ID: taskID},
		Method:  "tasks/send",
		Params:  json.RawMessage(paramsBytes),
	}

	t.Run("SendTask Success", func(t *testing.T) {
		// Prepare mock server success response.
		respTask := protocol.Task{
			ID:     taskID,
			Status: protocol.TaskStatus{State: protocol.TaskStateSubmitted},
		}
		respResultBytes, err := json.Marshal(respTask)
		require.NoError(t, err)
		respBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":"%s","result":%s}`, taskID, string(respResultBytes))

		mockHandler := createMockServerHandler(
			t,
			"tasks/send",
			expectedRequest,
			respBody,
			http.StatusOK,
			nil,
		)
		server := httptest.NewServer(mockHandler)
		defer server.Close()

		client, err := NewA2AClient(server.URL)
		require.NoError(t, err)

		// Call the client method.
		result, err := client.SendTasks(context.Background(), params)

		// Assertions.
		require.NoError(t, err, "SendTask should not return an error on success")
		require.NotNil(t, result, "Result should not be nil on success")

		assert.Equal(t, taskID, result.ID)
		assert.Equal(t, protocol.TaskStateSubmitted, result.Status.State)
	})

	t.Run("SendTask HTTP Error", func(t *testing.T) {
		// Prepare mock server HTTP error response.
		mockHandler := createMockServerHandler(
			t,
			"tasks/send",
			expectedRequest,
			"Internal Server Error",
			http.StatusInternalServerError,
			nil,
		)
		server := httptest.NewServer(mockHandler)
		defer server.Close()
		client, err := NewA2AClient(server.URL)
		require.NoError(t, err)

		// Call the client method.
		result, err := client.SendTasks(context.Background(), params)

		// Assertions.
		require.Error(t, err, "SendTask should return an error on HTTP error")
		assert.Nil(t, result, "Result should be nil on error")
		assert.Contains(t, err.Error(), "unexpected http status 500")
	})
}

// TestA2AClient_StreamTask tests the StreamTask client method for SSE.
// It covers success, HTTP errors, and non-SSE response scenarios.
func TestA2AClient_StreamTask(t *testing.T) {
	taskID := "client-task-sse-1"
	params := protocol.SendTaskParams{
		ID: taskID,
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("Client SSE test")},
		},
	}
	paramsBytes, err := json.Marshal(params)
	require.NoError(t, err)

	expectedRequest := &jsonrpc.Request{
		Message: jsonrpc.Message{JSONRPC: "2.0", ID: taskID},
		Method:  "tasks/sendSubscribe",
		Params:  json.RawMessage(paramsBytes),
	}

	t.Run("StreamTask Success", func(t *testing.T) {
		// Prepare mock SSE stream data.
		sseEvent1Data, _ := json.Marshal(protocol.TaskStatusUpdateEvent{
			ID:     taskID,
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
			Final:  false,
		})
		sseEvent2Data, _ := json.Marshal(protocol.TaskArtifactUpdateEvent{
			ID:       taskID,
			Artifact: protocol.Artifact{Index: 0, Parts: []protocol.Part{protocol.NewTextPart("SSE data")}},
			Final:    false,
		})
		sseEvent3Data, _ := json.Marshal(protocol.TaskStatusUpdateEvent{
			ID:     taskID,
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
			Final:  true,
		})

		// Format the mock SSE stream string.
		sseStream := fmt.Sprintf("event: task_status_update\ndata: %s\n\n"+
			"event: task_artifact_update\ndata: %s\n\n"+
			"event: task_status_update\ndata: %s\n\n",
			string(sseEvent1Data), string(sseEvent2Data), string(sseEvent3Data))

		// Define required SSE headers.
		sseHeaders := map[string]string{
			"Content-Type":  "text/event-stream",
			"Cache-Control": "no-cache",
			"Connection":    "keep-alive",
		}

		mockHandler := createMockServerHandler(
			t,
			"tasks/sendSubscribe",
			expectedRequest,
			sseStream,
			http.StatusOK,
			sseHeaders,
		)
		server := httptest.NewServer(mockHandler)
		defer server.Close()

		client, err := NewA2AClient(server.URL)
		require.NoError(t, err)

		// Call the client method.
		eventChan, err := client.StreamTask(context.Background(), params)

		// Assertions for successful stream initiation.
		require.NoError(t, err, "StreamTask should not return an error on success")
		require.NotNil(t, eventChan, "Event channel should not be nil")

		// Read events from channel with timeout.
		receivedEvents := []protocol.TaskEvent{}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	loop:
		for {
			select {
			case event, ok := <-eventChan:
				if !ok {
					break loop // Channel closed, exit loop.
				}
				receivedEvents = append(receivedEvents, event)
			case <-ctx.Done():
				t.Fatal("Timeout waiting for events from StreamTask channel")
			}
		}

		// Assert the content and order of received events.
		require.Len(t, receivedEvents, 3, "Should receive exactly 3 events")
		_, ok1 := receivedEvents[0].(protocol.TaskStatusUpdateEvent)
		_, ok2 := receivedEvents[1].(protocol.TaskArtifactUpdateEvent)
		_, ok3 := receivedEvents[2].(protocol.TaskStatusUpdateEvent)
		assert.True(t, ok1 && ok2 && ok3, "Received event types mismatch expected sequence")
		assert.Equal(t, protocol.TaskStateWorking,
			receivedEvents[0].(protocol.TaskStatusUpdateEvent).Status.State, "First event state mismatch")
		assert.False(t, receivedEvents[0].IsFinal(), "First event should not be final")
		assert.False(t, receivedEvents[1].IsFinal(), "Second event should not be final")
		assert.Equal(t, protocol.TaskStateCompleted,
			receivedEvents[2].(protocol.TaskStatusUpdateEvent).Status.State, "Third event state mismatch")
		assert.True(t, receivedEvents[2].IsFinal(), "Last event should be final")
	})

	t.Run("StreamTask HTTP Error", func(t *testing.T) {
		// Prepare mock server HTTP error response.
		mockHandler := createMockServerHandler(
			t, "tasks/sendSubscribe", expectedRequest, "Not Found", http.StatusNotFound, nil,
		)
		server := httptest.NewServer(mockHandler)
		defer server.Close()

		client, err := NewA2AClient(server.URL)
		require.NoError(t, err)

		// Call the client method.
		eventChan, err := client.StreamTask(context.Background(), params)

		// Assertions.
		require.Error(t, err, "StreamTask should return an error on HTTP error")
		assert.Nil(t, eventChan, "Event channel should be nil on error")
		assert.Contains(t, err.Error(), "unexpected http status 404")
	})

	t.Run("StreamTask Non-SSE Response", func(t *testing.T) {
		// Prepare mock server returning JSON instead of SSE.
		mockHandler := createMockServerHandler(
			t, "tasks/sendSubscribe", expectedRequest,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":"%s","result":"not an sse stream"}`, taskID),
			http.StatusOK,
			map[string]string{"Content-Type": "application/json"}, // Incorrect Content-Type for SSE.
		)
		server := httptest.NewServer(mockHandler)
		defer server.Close()

		client, err := NewA2AClient(server.URL)
		require.NoError(t, err)

		// Call the client method.
		eventChan, err := client.StreamTask(context.Background(), params)

		// Assertions.
		require.Error(t, err, "StreamTask should return an error on non-SSE response")
		assert.Nil(t, eventChan, "Event channel should be nil on error")
		assert.Contains(t, err.Error(), "server did not respond with Content-Type 'text/event-stream'")
	})
}

// createMockServerHandler provides a configurable mock HTTP handler for testing
// client interactions. It verifies the incoming request method, headers, and
// body (if expectedReqBody is provided) before sending a configured response.
func createMockServerHandler(
	t *testing.T,
	expectedMethod string,
	expectedReqBody *jsonrpc.Request,
	responseBody string,
	statusCode int,
	responseHeaders map[string]string,
) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		// Check method and content type header.
		assert.Equal(t, http.MethodPost, r.Method, "MockHandler: Expected POST method")
		assert.Equal(t, "application/json; charset=utf-8",
			r.Header.Get("Content-Type"), "MockHandler: Content-Type header mismatch")

		// Read request body.
		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err, "MockHandler: Failed to read request body")

		// Verify request body if expectedReqBody is provided.
		if expectedReqBody != nil {
			var receivedReq jsonrpc.Request
			err = json.Unmarshal(bodyBytes, &receivedReq)
			require.NoError(t, err, "MockHandler: Failed to unmarshal request body. Body: %s", string(bodyBytes))

			assert.Equal(t, expectedReqBody.JSONRPC, receivedReq.JSONRPC,
				"MockHandler: Request JSONRPC version mismatch")
			assert.Equal(t, expectedReqBody.ID, receivedReq.ID, "MockHandler: Request ID mismatch")
			assert.Equal(t, expectedMethod, receivedReq.Method, "MockHandler: Request method mismatch")
			assert.JSONEq(t, string(expectedReqBody.Params), string(receivedReq.Params),
				"MockHandler: Request params mismatch")
		}

		// Set response headers.
		if responseHeaders != nil {
			for k, v := range responseHeaders {
				w.Header().Set(k, v)
			}
		} else {
			// Default to JSON response header if none provided.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}

		// Write status code and response body.
		w.WriteHeader(statusCode)
		_, err = w.Write([]byte(responseBody))
		assert.NoError(t, err, "MockHandler: Failed to write response body")
	}
}
