// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestParseHTTPJSONRoute(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		basePath  string
		operation string
		tenant    string
		taskID    string
		configID  string
	}{
		{name: "send", path: "/message:send", operation: protocol.MethodMessageSend},
		{name: "stream tenant", path: "/tenant-a/message:stream", operation: protocol.MethodMessageStream, tenant: "tenant-a"},
		{name: "get task", path: "/tasks/task-1", operation: protocol.MethodTasksGet, taskID: "task-1"},
		{name: "list tasks under base", path: "/api/a2a/tasks", basePath: "/api/a2a", operation: protocol.MethodTasksList},
		{name: "cancel", path: "/tasks/task-1:cancel", operation: protocol.MethodTasksCancel, taskID: "task-1"},
		{name: "subscribe", path: "/tasks/task-1:subscribe", operation: protocol.MethodTasksResubscribe, taskID: "task-1"},
		{name: "create or list push", path: "/tasks/task-1/pushNotificationConfigs", operation: protocol.MethodTasksPushNotificationConfigList, taskID: "task-1"},
		{name: "get or delete push", path: "/tenant-a/tasks/task-1/pushNotificationConfigs/config-1", operation: protocol.MethodTasksPushNotificationConfigGet, tenant: "tenant-a", taskID: "task-1", configID: "config-1"},
		{name: "extended card", path: "/extendedAgentCard", operation: protocol.MethodAgentAuthenticatedExtendedCard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route, ok := parseHTTPJSONRoute(tt.path, tt.basePath)
			require.True(t, ok)
			assert.Equal(t, tt.operation, route.operation)
			assert.Equal(t, tt.tenant, route.tenant)
			assert.Equal(t, tt.taskID, route.taskID)
			assert.Equal(t, tt.configID, route.configID)
		})
	}
}

func TestHTTPJSONMediaTypesAndRawResponse(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	params := protocol.SendMessageParams{Message: protocol.Message{
		MessageID: "message-1",
		Role:      protocol.MessageRoleUser,
		Parts:     []*protocol.Part{protocol.NewTextPart("hello")},
	}}
	body, err := json.Marshal(params)
	require.NoError(t, err)

	for _, contentType := range []string{
		protocol.MediaTypeA2AJSON + "; charset=utf-8",
		protocol.MediaTypeJSON,
	} {
		t.Run(contentType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()
			srv.Handler().ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, protocol.MediaTypeA2AJSON, recorder.Header().Get("Content-Type"))
			mediaType, _, parseErr := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
			require.NoError(t, parseErr)
			assert.Equal(t, protocol.MediaTypeA2AJSON, mediaType)
			var response map[string]any
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Contains(t, response, "message")
			assert.NotContains(t, response, "jsonrpc")
			assert.NotContains(t, response, "result")
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "CONTENT_TYPE_NOT_SUPPORTED")

	req = httptest.NewRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	req.Header.Set("A2A-Version", "2.0")
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "VERSION_NOT_SUPPORTED")

	req = httptest.NewRequest(http.MethodPost, "/message:send?A2A-Version=2.0", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "VERSION_NOT_SUPPORTED")
}

func TestHTTPJSONTaskNotFoundError(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/tasks/missing-task", nil)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	var response struct {
		Error struct {
			Code    int              `json:"code"`
			Status  string           `json:"status"`
			Details []map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, http.StatusNotFound, response.Error.Code)
	assert.Equal(t, "NOT_FOUND", response.Error.Status)
	require.Len(t, response.Error.Details, 1)
	assert.Equal(t, "TASK_NOT_FOUND", response.Error.Details[0]["reason"])
}

func TestHTTPJSONStreamUsesRawSSEData(t *testing.T) {
	manager := newMockTaskManager()
	message := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart("hello")})
	manager.sendMessageStreamEvents = []protocol.StreamResponse{{Result: &message}}
	srv, err := NewA2AServer(
		manager,
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	params := protocol.SendMessageParams{Message: protocol.Message{
		MessageID: "message-1",
		Role:      protocol.MessageRoleUser,
		Parts:     []*protocol.Part{protocol.NewTextPart("hello")},
	}}
	body, err := json.Marshal(params)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/message:stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, protocol.MediaTypeEventStream, recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), "data: {\"message\":")
	assert.NotContains(t, recorder.Body.String(), "jsonrpc")
	assert.NotContains(t, recorder.Body.String(), "\"result\"")
}
