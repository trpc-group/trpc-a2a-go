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
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

func newV1HTTPRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("A2A-Version", protocol.ProtocolVersionV1)
	return req
}

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
		// Literal paths remain compatible with reference clients using the
		// tenant="tasks" additional binding.
		{name: "tenant named tasks sends", path: "/tasks/message:send", operation: protocol.MethodMessageSend, tenant: "tasks"},
		{name: "tenant named tasks lists", path: "/tasks/tasks", operation: protocol.MethodTasksList, tenant: "tasks"},
		{name: "tenant named tasks extended card", path: "/tasks/extendedAgentCard", operation: protocol.MethodAgentAuthenticatedExtendedCard, tenant: "tasks"},
		// Escaped task IDs stay in the tenant-less namespace.
		{name: "task id message send", path: "/tasks/message%3Asend", operation: protocol.MethodTasksGet, taskID: "message:send"},
		{name: "task id tasks", path: "/tasks/%74asks", operation: protocol.MethodTasksGet, taskID: "tasks"},
		{name: "task id extended card", path: "/tasks/%65xtendedAgentCard", operation: protocol.MethodTasksGet, taskID: "extendedAgentCard"},
		// A percent-encoded colon is part of the ID, not an operation verb.
		{name: "task id with escaped colon", path: "/tasks/job%3Acancel", operation: protocol.MethodTasksGet, taskID: "job:cancel"},
		{name: "escaped colon subscribe", path: "/tasks/job%3Asubscribe", operation: protocol.MethodTasksGet, taskID: "job:subscribe"},
		{name: "literal colon is a verb", path: "/tasks/job:cancel", operation: protocol.MethodTasksCancel, taskID: "job"},
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

func TestParseHTTPJSONRouteRejects(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		basePath string
	}{
		{name: "empty segment", path: "/tasks//task-1"},
		{name: "outside base path", path: "/other/tasks", basePath: "/api/a2a"},
		{name: "adjacent to base path", path: "/api/a2atasks", basePath: "/api/a2a"},
		{name: "unknown sub-collection", path: "/tasks/task-1/unknown"},
		{name: "too many segments", path: "/tasks/task-1/pushNotificationConfigs/config-1/extra"},
		{name: "unknown operation", path: "/unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseHTTPJSONRoute(tt.path, tt.basePath)
			assert.False(t, ok)
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
			req := newV1HTTPRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
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

	req := newV1HTTPRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "CONTENT_TYPE_NOT_SUPPORTED")

	req = newV1HTTPRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	req.Header.Set("A2A-Version", "2.0")
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "VERSION_NOT_SUPPORTED")

	req = newV1HTTPRequest(http.MethodPost, "/message:send?A2A-Version=2.0", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	req.Header.Del("A2A-Version")
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "VERSION_NOT_SUPPORTED")

	// An absent version is interpreted as 0.3, which this v1-only interface
	// does not support.
	req = httptest.NewRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "VERSION_NOT_SUPPORTED")

	for _, version := range []string{"1.0.0", "1.0.7"} {
		req = newV1HTTPRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
		req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
		req.Header.Set("A2A-Version", version)
		recorder = httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusOK, recorder.Code, version)
	}
}

func TestHTTPJSONTaskNotFoundError(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	req := newV1HTTPRequest(http.MethodGet, "/tasks/missing-task", nil)
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

// error.message carries the human-readable cause (spec §11.6), so the detail
// the A2A constructors keep in Data must reach the wire rather than the generic
// heading the JSON-RPC binding puts in its "message" field.
func TestHTTPJSONErrorBodyCarriesDetail(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	params := protocol.SendMessageParams{Message: protocol.Message{
		MessageID: "message-1",
		Role:      protocol.MessageRoleAgent,
		Parts:     []*protocol.Part{protocol.NewTextPart("hello")},
	}}
	body, err := json.Marshal(params)
	require.NoError(t, err)
	req := newV1HTTPRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var response struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "INVALID_ARGUMENT", response.Error.Status)
	assert.Equal(t, "message role must be ROLE_USER", response.Error.Message)

	// A tenant conflict is likewise reported by cause, not as bare "Invalid params".
	req = newV1HTTPRequest(http.MethodPost, "/tenant-a/message:send", bytes.NewReader(
		[]byte(`{"tenant":"tenant-b","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}`)))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "tenant in request does not match tenant in URL path")
}

// Advertising HTTP+JSON on the card does not enable or relocate the REST
// binding; WithHTTPJSONEndpoint is required. JSON-RPC stays on the default path.
func TestHTTPJSONRequiresExplicitEndpointOption(t *testing.T) {
	transport := protocol.ProtocolBindingHTTPJSON
	card := defaultAgentCard()
	card.SupportedInterfaces = nil
	card.AdditionalInterfaces = nil
	card.URL = "https://host.example/agent"
	card.PreferredTransport = &transport

	srv, err := NewA2AServer(newMockTaskManager(), WithAgentCard(card))
	require.NoError(t, err)
	assert.Equal(t, "/", srv.jsonRPCEndpoint)
	assert.Equal(t, "", srv.httpJSONBasePath)
	assert.False(t, srv.httpJSONEnabled)

	rpcBody := []byte(`{"jsonrpc":"2.0","id":"1","method":"` + protocol.MethodTasksGet + `","params":{"id":"missing-task"}}`)
	req := newV1HTTPRequest(http.MethodPost, "/", bytes.NewReader(rpcBody))
	req.Header.Set("Content-Type", protocol.MediaTypeJSON)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Contains(t, recorder.Body.String(), "jsonrpc")

	srv, err = NewA2AServer(newMockTaskManager(), WithAgentCard(card), WithHTTPJSONEndpoint("/agent"))
	require.NoError(t, err)
	assert.True(t, srv.httpJSONEnabled)
	assert.Equal(t, "/agent", srv.httpJSONBasePath)

	req = newV1HTTPRequest(http.MethodGet, "/agent/tasks/missing-task", nil)
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "TASK_NOT_FOUND")
}

// A literal tenant named "tasks" follows the official additional binding.
// Conflicting tenant-less task IDs use an escaped ID segment instead.
func TestHTTPJSONTenantShadowingRouteKeyword(t *testing.T) {
	srv, err := NewA2AServer(
		newMockTaskManager(),
		WithAgentCard(defaultAgentCard()),
		WithTenantCard("tasks", defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)

	body := []byte(`{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}`)
	req := newV1HTTPRequest(http.MethodPost, "/tasks/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)
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
	req := newV1HTTPRequest(http.MethodPost, "/message:stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, protocol.MediaTypeEventStream, recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), "data: {\"message\":")
	assert.NotContains(t, recorder.Body.String(), "jsonrpc")
	assert.NotContains(t, recorder.Body.String(), "\"result\"")
}

func TestHTTPJSONTaskPushAndExtendedCardRoutes(t *testing.T) {
	manager := newMockTaskManager()
	manager.pushSupported = true
	manager.tasks["task-1"] = &protocol.Task{
		ID:        "task-1",
		ContextID: "context-1",
		Status:    protocol.TaskStatus{State: protocol.TaskStateWorking},
	}
	manager.pushNotificationGetResponse = &protocol.TaskPushNotificationConfig{
		ID:     "config-1",
		TaskID: "task-1",
		URL:    "https://example.com/webhook",
	}
	extended := true
	card := defaultAgentCard()
	card.Capabilities.ExtendedAgentCard = &extended
	srv, err := NewA2AServer(
		manager,
		WithAgentCard(card),
		WithHTTPJSONEndpoint("/"),
		WithAuthenticatedExtendedCardHandler(func(_ context.Context, base AgentCard) (AgentCard, error) {
			return base, nil
		}),
	)
	require.NoError(t, err)

	request := func(method, target string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req := newV1HTTPRequest(method, target, bytes.NewReader(encoded))
		if body != nil {
			req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
		}
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, req)
		return recorder
	}

	recorder := request(
		http.MethodGet,
		"/tasks?contextId=context-1&status=TASK_STATE_WORKING&pageSize=5&historyLength=2&includeArtifacts=true",
		nil,
	)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "\"tasks\"")

	recorder = request(http.MethodGet, "/tasks?pageSize=invalid", nil)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	recorder = request(http.MethodGet, "/tasks?includeArtifacts=invalid", nil)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	recorder = request(http.MethodPost, "/tasks/task-1:cancel", protocol.TaskIDParams{ID: "task-1"})
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "TASK_STATE_CANCELED")
	recorder = request(http.MethodPost, "/tasks/task-1:cancel", protocol.TaskIDParams{ID: "other-task"})
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	recorder = request(http.MethodPost, "/tasks/task-1:subscribe", nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, protocol.MediaTypeEventStream, recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), "data:")
	recorder = request(http.MethodGet, "/tasks/task-1:subscribe", nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, protocol.MediaTypeEventStream, recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), "data:")

	config := protocol.TaskPushNotificationConfig{
		ID:     "config-1",
		TaskID: "task-1",
		URL:    "https://example.com/webhook",
	}
	recorder = request(http.MethodPost, "/tasks/task-1/pushNotificationConfigs", config)
	// The proto transcoding rule returns the created config with 200.
	assert.Equal(t, http.StatusOK, recorder.Code)
	recorder = request(http.MethodGet, "/tasks/task-1/pushNotificationConfigs/config-1", nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	recorder = request(http.MethodGet, "/tasks/task-1/pushNotificationConfigs?pageSize=5&pageToken=next", nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	recorder = request(http.MethodDelete, "/tasks/task-1/pushNotificationConfigs/config-1", nil)
	// Delete returns google.protobuf.Empty, i.e. 200 with an empty object.
	assert.Equal(t, http.StatusOK, recorder.Code)

	recorder = request(http.MethodGet, "/extendedAgentCard", nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Test Agent")

	recorder = request(http.MethodPut, "/tasks", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Equal(t, http.MethodGet, recorder.Header().Get("Allow"))
	recorder = request(http.MethodGet, "/unknown", nil)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestHTTPJSONValidationAndCapabilityErrors(t *testing.T) {
	manager := newMockTaskManager()
	srv, err := NewA2AServer(manager, WithAgentCard(defaultAgentCard()), WithHTTPJSONEndpoint("/"))
	require.NoError(t, err)

	request := func(method, target, body string) *httptest.ResponseRecorder {
		req := newV1HTTPRequest(method, target, bytes.NewBufferString(body))
		if body != "" {
			req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
		}
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, req)
		return recorder
	}

	recorder := request(http.MethodPost, "/message:send", "")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	recorder = request(http.MethodPost, "/message:send", `{}`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	recorder = request(http.MethodPost, "/message:send", `{"message":{"role":"ROLE_AGENT","parts":[{"text":"bad"}]}}`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	recorder = request(http.MethodPost, "/tasks/task-1:cancel", `{invalid`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	recorder = request(http.MethodPost, "/tasks/task-1:cancel", `{} trailing`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "failed to parse request body")
	recorder = request(http.MethodPost, "/tasks/task-1:cancel", `{} {}`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "request body must contain exactly one JSON value")
	recorder = request(http.MethodPost, "/tenant-a/tasks/task-1:cancel", `{"tenant":"tenant-b"}`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	recorder = request(
		http.MethodPost,
		"/tasks/task-1/pushNotificationConfigs",
		`{"taskId":"task-1","url":"not-a-url"}`,
	)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "PUSH_NOTIFICATION_NOT_SUPPORTED")
	recorder = request(http.MethodGet, "/tasks/task-1/pushNotificationConfigs/config-1", "")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "PUSH_NOTIFICATION_NOT_SUPPORTED")

	recorder = request(http.MethodGet, "/extendedAgentCard", "")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "UNSUPPORTED_OPERATION")

	limited, err := NewA2AServer(
		manager,
		WithAgentCard(defaultAgentCard()),
		WithHTTPJSONEndpoint("/"),
		WithHTTPJSONMaxBodyBytes(32),
	)
	require.NoError(t, err)
	req := newV1HTTPRequest(http.MethodPost, "/message:send", strings.NewReader(strings.Repeat(" ", 33)))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder = httptest.NewRecorder()
	limited.Handler().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "request body exceeds 32 bytes")
}

func TestDecodeHTTPJSONBodyLimitCanBeDisabled(t *testing.T) {
	srv := &A2AServer{httpJSONMaxBody: defaultHTTPJSONMaxBodyBytes}
	WithHTTPJSONMaxBodyBytes(0)(srv)
	req := newV1HTTPRequest(http.MethodPost, "/", strings.NewReader(`{"value":"accepted"}`))

	var body map[string]string
	err := srv.decodeHTTPJSONBody(httptest.NewRecorder(), req, &body, true)

	require.NoError(t, err)
	assert.Equal(t, "accepted", body["value"])
}

func TestHTTPJSONErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		status     string
		reason     string
	}{
		{name: "plain error", err: errors.New("boom"), statusCode: http.StatusInternalServerError, status: "INTERNAL"},
		{name: "invalid params", err: taskmanager.ErrInvalidParams("bad"), statusCode: http.StatusBadRequest, status: "INVALID_ARGUMENT"},
		{name: "task not found", err: taskmanager.ErrTaskNotFound("task"), statusCode: http.StatusNotFound, status: "NOT_FOUND", reason: "TASK_NOT_FOUND"},
		{name: "not cancelable", err: taskmanager.ErrTaskNotCancelable("task", protocol.TaskStateCompleted), statusCode: http.StatusBadRequest, status: "FAILED_PRECONDITION", reason: "TASK_NOT_CANCELABLE"},
		{name: "push unsupported", err: taskmanager.ErrPushNotificationNotSupported(), statusCode: http.StatusBadRequest, status: "FAILED_PRECONDITION", reason: "PUSH_NOTIFICATION_NOT_SUPPORTED"},
		{name: "unsupported", err: taskmanager.ErrUnsupportedOperation("operation"), statusCode: http.StatusBadRequest, status: "FAILED_PRECONDITION", reason: "UNSUPPORTED_OPERATION"},
		{name: "content type", err: taskmanager.ErrContentTypeNotSupported("type"), statusCode: http.StatusBadRequest, status: "INVALID_ARGUMENT", reason: "CONTENT_TYPE_NOT_SUPPORTED"},
		{name: "invalid agent response", err: taskmanager.ErrInvalidAgentResponse("bad"), statusCode: http.StatusInternalServerError, status: "INTERNAL", reason: "INVALID_AGENT_RESPONSE"},
		{name: "extended card", err: taskmanager.ErrAuthenticatedExtendedCardNotConfigured(), statusCode: http.StatusBadRequest, status: "FAILED_PRECONDITION", reason: "EXTENDED_AGENT_CARD_NOT_CONFIGURED"},
		{name: "extension", err: taskmanager.ErrExtensionSupportRequired("extension"), statusCode: http.StatusBadRequest, status: "FAILED_PRECONDITION", reason: "EXTENSION_SUPPORT_REQUIRED"},
		{name: "version", err: taskmanager.ErrVersionNotSupported("2.0"), statusCode: http.StatusBadRequest, status: "FAILED_PRECONDITION", reason: "VERSION_NOT_SUPPORTED"},
		{name: "internal", err: taskmanager.ErrInternalError("boom"), statusCode: http.StatusInternalServerError, status: "INTERNAL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statusCode, status, reason, message := httpJSONErrorMapping(tt.err)
			assert.Equal(t, tt.statusCode, statusCode)
			assert.Equal(t, tt.status, status)
			assert.Equal(t, tt.reason, reason)
			assert.NotEmpty(t, message)
		})
	}
}

// A server-side failure must not put its cause on the wire: the detail is
// logged and the client sees only the generic heading.
func TestHTTPJSONInternalErrorDetailIsNotLeaked(t *testing.T) {
	manager := newMockTaskManager()
	manager.sendMessageError = taskmanager.ErrInternalError(
		"dial tcp 10.0.0.7:5432: connect: connection refused (password=hunter2)")
	srv, err := NewA2AServer(manager, WithAgentCard(defaultAgentCard()), WithHTTPJSONEndpoint("/"))
	require.NoError(t, err)

	body, err := json.Marshal(protocol.SendMessageParams{Message: protocol.Message{
		MessageID: "message-1",
		Role:      protocol.MessageRoleUser,
		Parts:     []*protocol.Part{protocol.NewTextPart("hello")},
	}})
	require.NoError(t, err)
	req := newV1HTTPRequest(http.MethodPost, "/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "hunter2")
	assert.NotContains(t, recorder.Body.String(), "10.0.0.7")
	assert.Contains(t, recorder.Body.String(), "Internal error")
}
