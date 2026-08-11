// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/stateless"
)

type httpJSONEchoProcessor struct{}

func (httpJSONEchoProcessor) ProcessMessage(
	_ context.Context,
	execContext *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	events := make(chan protocol.StreamEvent, 1)
	reply := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]*protocol.Part{protocol.NewTextPart("echo: " + execContext.Message.Parts[0].TextContent())},
	)
	events <- &reply
	close(events)
	return events, nil
}

func TestHTTPJSONClientServerSendAndStream(t *testing.T) {
	manager, err := stateless.NewTaskManager(httpJSONEchoProcessor{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	streaming := true
	card := protocol.AgentCard{
		Name:               "HTTP+JSON Agent",
		Description:        "test",
		Version:            "1",
		Capabilities:       protocol.AgentCapabilities{Streaming: &streaming},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []protocol.AgentSkill{},
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             "http://placeholder.invalid",
			ProtocolBinding: protocol.ProtocolBindingHTTPJSON,
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
	}
	a2aServer, err := server.NewA2AServer(
		manager,
		server.WithAgentCard(card),
		server.WithHTTPJSONEndpoint("/"),
	)
	require.NoError(t, err)
	testServer := httptest.NewServer(a2aServer.Handler())
	t.Cleanup(testServer.Close)

	client, err := NewA2AClient(testServer.URL, WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
	require.NoError(t, err)
	assert.Equal(t, protocol.ProtocolBindingHTTPJSON, client.protocolBinding)

	params := protocol.SendMessageParams{Message: protocol.Message{
		MessageID: "message-1",
		Role:      protocol.MessageRoleUser,
		Parts:     []*protocol.Part{protocol.NewTextPart("hello")},
	}}
	response, err := client.SendMessage(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, response.GetMessage())
	assert.Equal(t, "echo: hello", response.GetMessage().Parts[0].TextContent())

	events, err := client.StreamMessage(context.Background(), params)
	require.NoError(t, err)
	var streamed []protocol.StreamResponse
	for event := range events {
		streamed = append(streamed, event)
	}
	require.Len(t, streamed, 1)
	require.NotNil(t, streamed[0].GetMessage())
	assert.Equal(t, "echo: hello", streamed[0].GetMessage().Parts[0].TextContent())
}

func TestWithProtocolBindingSelectsHTTPJSON(t *testing.T) {
	client, err := NewA2AClient(
		"https://rest.example/api",
		WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
		WithTenant("tenant-a"),
	)
	require.NoError(t, err)
	assert.Equal(t, protocol.ProtocolBindingHTTPJSON, client.protocolBinding)
	assert.Equal(t, "tenant-a", client.tenant)
	assert.Equal(t, "https://rest.example/api/", client.baseURL.String())

	_, err = NewA2AClient("https://rest.example/api", WithProtocolBinding("CUSTOM"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported A2A protocol binding")
}

func TestHTTPJSONRequestHeadersAndBody(t *testing.T) {
	var requestErr error
	var requestErrMu sync.Mutex
	recordRequestErr := func(err error) {
		requestErrMu.Lock()
		defer requestErrMu.Unlock()
		requestErr = err
	}
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			recordRequestErr(fmt.Errorf("method = %s", r.Method))
		}
		// The tenant is a path segment, matching the proto's additional binding.
		if r.URL.EscapedPath() != "/tenant%2Fa/message:send" {
			recordRequestErr(fmt.Errorf("path = %s", r.URL.EscapedPath()))
		}
		if r.URL.RawQuery != "" {
			recordRequestErr(fmt.Errorf("query = %s", r.URL.RawQuery))
		}
		if r.Header.Get("Content-Type") != protocol.MediaTypeA2AJSON {
			recordRequestErr(fmt.Errorf("Content-Type = %s", r.Header.Get("Content-Type")))
		}
		if r.Header.Get("Accept") != protocol.MediaTypeA2AJSON+", "+protocol.MediaTypeJSON {
			recordRequestErr(fmt.Errorf("Accept = %s", r.Header.Get("Accept")))
		}
		if r.Header.Get("A2A-Version") != protocol.ProtocolVersionV1 {
			recordRequestErr(fmt.Errorf("A2A-Version = %s", r.Header.Get("A2A-Version")))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			recordRequestErr(err)
		}
		if _, ok := body["jsonrpc"]; ok {
			recordRequestErr(fmt.Errorf("request contains JSON-RPC envelope"))
		}
		if body["tenant"] != "tenant/a" {
			recordRequestErr(fmt.Errorf("body tenant = %v", body["tenant"]))
		}
		w.Header().Set("Content-Type", protocol.MediaTypeA2AJSON)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": body["message"]})
	}))
	t.Cleanup(testServer.Close)

	client, err := NewA2AClient(
		testServer.URL,
		WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
		WithTenant("tenant/a"),
	)
	require.NoError(t, err)
	_, err = client.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: protocol.Message{
			MessageID: "message-1",
			Role:      protocol.MessageRoleUser,
			Parts:     []*protocol.Part{protocol.NewTextPart("hello")},
		},
	})
	require.NoError(t, err)
	requestErrMu.Lock()
	defer requestErrMu.Unlock()
	require.NoError(t, requestErr)

	_, err = client.SendMessage(context.Background(), protocol.SendMessageParams{Tenant: "another-tenant"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match configured client tenant")
}

func TestHTTPJSONURLPreservesEndpointQuery(t *testing.T) {
	client, err := NewA2AClient(
		"https://rest.example/api?access_token=secret&region=base",
		WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
	)
	require.NoError(t, err)
	assert.Equal(t, "secret", client.baseURL.Query().Get("access_token"))
	assert.Equal(t, "base", client.baseURL.Query().Get("region"))

	target, err := url.Parse(client.httpJSONURL("/tasks", url.Values{
		"pageSize": {"10"},
		"region":   {"request"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "/api/tasks", target.EscapedPath())
	assert.Equal(t, "secret", target.Query().Get("access_token"))
	assert.Equal(t, "10", target.Query().Get("pageSize"))
	assert.Equal(t, "request", target.Query().Get("region"), "operation query overrides endpoint defaults")
}

func TestBuildHTTPJSONRequestRoutes(t *testing.T) {
	pageSize := 10
	tests := []struct {
		name      string
		operation string
		params    any
		method    string
		path      string
		query     string
		body      bool
	}{
		{name: "send", operation: protocol.MethodMessageSend, params: protocol.SendMessageParams{Tenant: "tenant"}, method: http.MethodPost, path: "/tenant/message:send", body: true},
		{name: "stream", operation: protocol.MethodMessageStream, params: protocol.SendMessageParams{}, method: http.MethodPost, path: "/message:stream", body: true},
		{name: "get task", operation: protocol.MethodTasksGet, params: protocol.TaskQueryParams{ID: "task/a", HistoryLength: &pageSize}, method: http.MethodGet, path: "/tasks/task%2Fa", query: "historyLength=10"},
		{name: "list tasks", operation: protocol.MethodTasksList, params: protocol.ListTasksParams{ContextID: "ctx", PageSize: &pageSize}, method: http.MethodGet, path: "/tasks", query: "contextId=ctx&pageSize=10"},
		{name: "cancel", operation: protocol.MethodTasksCancel, params: protocol.TaskIDParams{ID: "task"}, method: http.MethodPost, path: "/tasks/task:cancel", body: true},
		{name: "subscribe", operation: protocol.MethodTasksResubscribe, params: protocol.TaskIDParams{ID: "task"}, method: http.MethodPost, path: "/tasks/task:subscribe"},
		{name: "create push", operation: protocol.MethodTasksPushNotificationConfigSet, params: protocol.TaskPushNotificationConfig{TaskID: "task"}, method: http.MethodPost, path: "/tasks/task/pushNotificationConfigs", body: true},
		{name: "get push", operation: protocol.MethodTasksPushNotificationConfigGet, params: protocol.GetTaskPushNotificationConfigParams{TaskID: "task", ID: "config"}, method: http.MethodGet, path: "/tasks/task/pushNotificationConfigs/config"},
		{name: "list push", operation: protocol.MethodTasksPushNotificationConfigList, params: protocol.ListTaskPushNotificationConfigsParams{TaskID: "task", PageSize: &pageSize}, method: http.MethodGet, path: "/tasks/task/pushNotificationConfigs", query: "pageSize=10"},
		{name: "delete push", operation: protocol.MethodTasksPushNotificationConfigDelete, params: protocol.DeleteTaskPushNotificationConfigParams{TaskID: "task", ID: "config"}, method: http.MethodDelete, path: "/tasks/task/pushNotificationConfigs/config"},
		{name: "extended card", operation: protocol.MethodAgentAuthenticatedExtendedCard, params: extendedCardParams{Tenant: "tenant"}, method: http.MethodGet, path: "/tenant/extendedAgentCard"},
		{name: "get task tenant", operation: protocol.MethodTasksGet, params: protocol.TaskQueryParams{ID: "task", Tenant: "tenant"}, method: http.MethodGet, path: "/tenant/tasks/task"},
		{name: "reserved tenant", operation: protocol.MethodTasksList, params: protocol.ListTasksParams{Tenant: "tasks"}, method: http.MethodGet, path: "/tasks/tasks"},
		{name: "reserved task id", operation: protocol.MethodTasksGet, params: protocol.TaskQueryParams{ID: "tasks"}, method: http.MethodGet, path: "/tasks/%74asks"},
		{name: "extended card task id", operation: protocol.MethodTasksGet, params: protocol.TaskQueryParams{ID: "extendedAgentCard"}, method: http.MethodGet, path: "/tasks/%65xtendedAgentCard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := buildHTTPJSONRequest(tt.operation, tt.params)
			require.NoError(t, err)
			assert.Equal(t, tt.method, spec.method)
			assert.Equal(t, tt.path, spec.path)
			assert.Equal(t, tt.query, spec.query.Encode())
			assert.Equal(t, tt.body, spec.body != nil)
		})
	}
}

func TestBuildHTTPJSONRequestErrors(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		params    any
	}{
		{name: "send type", operation: protocol.MethodMessageSend, params: struct{}{}},
		{name: "get type", operation: protocol.MethodTasksGet, params: struct{}{}},
		{name: "get metadata", operation: protocol.MethodTasksGet, params: protocol.TaskQueryParams{Metadata: map[string]any{"key": "value"}}},
		{name: "list type", operation: protocol.MethodTasksList, params: struct{}{}},
		{name: "list metadata", operation: protocol.MethodTasksList, params: protocol.ListTasksParams{Metadata: map[string]any{"key": "value"}}},
		{name: "cancel type", operation: protocol.MethodTasksCancel, params: struct{}{}},
		{name: "subscribe type", operation: protocol.MethodTasksResubscribe, params: struct{}{}},
		{name: "subscribe metadata", operation: protocol.MethodTasksResubscribe, params: protocol.TaskIDParams{Metadata: map[string]any{"key": "value"}}},
		{name: "create push type", operation: protocol.MethodTasksPushNotificationConfigSet, params: struct{}{}},
		{name: "get push type", operation: protocol.MethodTasksPushNotificationConfigGet, params: struct{}{}},
		{name: "list push type", operation: protocol.MethodTasksPushNotificationConfigList, params: struct{}{}},
		{name: "delete push type", operation: protocol.MethodTasksPushNotificationConfigDelete, params: struct{}{}},
		{name: "unsupported", operation: "unsupported", params: struct{}{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildHTTPJSONRequest(tt.operation, tt.params)
			require.Error(t, err)
		})
	}
}

func TestHTTPJSONBindingRequestValidation(t *testing.T) {
	tests := []struct {
		name        string
		handle      func(context.Context, *http.Client, *http.Request) (*http.Response, error)
		result      any
		expectError string
	}{
		{
			name: "handler error",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return nil, errors.New("request failed")
			},
			expectError: "HTTP+JSON request failed",
		},
		{
			name: "nil response",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return nil, nil
			},
			expectError: "unexpected nil response",
		},
		{
			name: "nil body",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK}, nil
			},
			expectError: "unexpected nil response",
		},
		{
			name: "error response",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`)),
				}, nil
			},
			expectError: "bad request",
		},
		{
			name: "no content",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
			},
		},
		{
			name: "nil result",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			},
			result: nil,
		},
		{
			name: "invalid content type",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/plain"}},
					Body:       io.NopCloser(strings.NewReader(`{}`)),
				}, nil
			},
			result:      &map[string]any{},
			expectError: "unexpected HTTP+JSON response Content-Type",
		},
		{
			name: "invalid JSON",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{protocol.MediaTypeJSON}},
					Body:       io.NopCloser(strings.NewReader(`{`)),
				}, nil
			},
			result:      &map[string]any{},
			expectError: "failed to decode HTTP+JSON response",
		},
		{
			name: "success",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{protocol.MediaTypeA2AJSON + "; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				}, nil
			},
			result: &map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewA2AClient(
				"https://example.com/a2a",
				WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
				WithHTTPReqHandler(&mockHTTPReqHandler{handleFunc: tt.handle}),
			)
			require.NoError(t, err)
			result := tt.result
			if result == nil && tt.name != "nil result" {
				result = &map[string]any{}
			}
			err = httpJSONBinding{}.request(
				context.Background(), client, "request-id", protocol.MethodMessageSend,
				protocol.SendMessageParams{}, result,
			)
			if tt.expectError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

func TestHTTPJSONBindingStreamValidation(t *testing.T) {
	tests := []struct {
		name        string
		handle      func(context.Context, *http.Client, *http.Request) (*http.Response, error)
		expectError string
	}{
		{
			name: "handler error",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return nil, errors.New("stream failed")
			},
			expectError: "HTTP+JSON stream request failed",
		},
		{
			name: "nil response",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return nil, nil
			},
			expectError: "unexpected nil response",
		},
		{
			name: "nil body",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK}, nil
			},
			expectError: "unexpected nil response",
		},
		{
			name: "error response",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"failed"}}`)),
				}, nil
			},
			expectError: "failed",
		},
		{
			name: "invalid content type",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{protocol.MediaTypeJSON}},
					Body:       http.NoBody,
				}, nil
			},
			expectError: "server did not respond with Content-Type",
		},
		{
			name: "success",
			handle: func(context.Context, *http.Client, *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{protocol.MediaTypeEventStream + "; charset=utf-8"}},
					Body:       http.NoBody,
				}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewA2AClient(
				"https://example.com/a2a",
				WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
				WithHTTPReqHandler(&mockHTTPReqHandler{handleFunc: tt.handle}),
			)
			require.NoError(t, err)
			resp, err := httpJSONBinding{}.stream(
				context.Background(), client, "request-id", protocol.MethodMessageStream,
				protocol.SendMessageParams{},
			)
			if tt.expectError == "" {
				require.NoError(t, err)
				require.NotNil(t, resp)
				require.NoError(t, resp.Body.Close())
				return
			}
			require.Error(t, err)
			assert.Nil(t, resp)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}

	_, err := httpJSONBinding{}.decodeSSEData([]byte(" \n\t"))
	require.Error(t, err)
	data, err := httpJSONBinding{}.decodeSSEData([]byte(`{"message":{}}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":{}}`, string(data))
}

func TestHTTPJSONRequestConstructionErrors(t *testing.T) {
	client, err := NewA2AClient("https://example.com/base", WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
	require.NoError(t, err)

	_, err = client.newHTTPJSONRequest(context.Background(), httpJSONRequest{
		method: http.MethodPost,
		path:   "/message:send",
		body:   func() {},
	}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal HTTP+JSON request")

	assert.NoError(t, validateHTTPJSONResponseType(protocol.MediaTypeJSON+"; charset=utf-8"))
	assert.NoError(t, validateHTTPJSONResponseType(protocol.MediaTypeA2AJSON))
	require.Error(t, validateHTTPJSONResponseType("not a media type"))
}

func TestJSONRPCBindingDecodeSSEData(t *testing.T) {
	binding := jsonRPCBinding{}

	_, err := binding.decodeSSEData([]byte(`{`))
	require.Error(t, err)

	raw := []byte(`{"message":{"role":"agent","parts":[]}}`)
	decoded, err := binding.decodeSSEData(raw)
	require.NoError(t, err)
	assert.Equal(t, raw, decoded)

	_, err = binding.decodeSSEData([]byte(`{"jsonrpc":"1.0","result":{}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON-RPC SSE response version")

	_, err = binding.decodeSSEData([]byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"failed"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jsonrpc error -32603")

	_, err = binding.decodeSSEData([]byte(`{"jsonrpc":"2.0"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing result")

	decoded, err = binding.decodeSSEData([]byte(`{"jsonrpc":"2.0","result":{"task":{"id":"task-1"}}}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"task":{"id":"task-1"}}`, string(decoded))
}

func TestDecodeHTTPJSONError(t *testing.T) {
	errorBody := func(code taskmanager.ErrorCode) string {
		return fmt.Sprintf(`{"error":{"message":"failed","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":%q,"metadata":{"taskId":"task-1"}}]}}`, code)
	}
	tests := []struct {
		name       string
		statusCode int
		body       string
		sentinel   error
	}{
		{name: "malformed", statusCode: http.StatusBadRequest, body: `{`, sentinel: nil},
		{name: "missing message", statusCode: http.StatusBadRequest, body: `{}`, sentinel: nil},
		{name: "invalid params", statusCode: http.StatusBadRequest, body: `{"error":{"message":"invalid"}}`, sentinel: taskmanager.ErrInvalidParamsSentinel},
		{name: "internal", statusCode: http.StatusInternalServerError, body: `{"error":{"message":"failed"}}`, sentinel: taskmanager.ErrInternalErrorSentinel},
		{name: "task not found", statusCode: http.StatusNotFound, body: errorBody(taskmanager.ErrCodeTaskNotFound), sentinel: taskmanager.ErrTaskNotFoundSentinel},
		{name: "task not cancelable", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeTaskNotCancelable), sentinel: taskmanager.ErrTaskNotCancelableSentinel},
		{name: "push unsupported", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodePushNotificationNotSupported), sentinel: taskmanager.ErrPushNotificationNotSupportedSentinel},
		{name: "operation unsupported", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeUnsupportedOperation), sentinel: taskmanager.ErrUnsupportedOperationSentinel},
		{name: "content type", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeContentTypeNotSupported), sentinel: taskmanager.ErrContentTypeNotSupportedSentinel},
		{name: "invalid response", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeInvalidAgentResponse), sentinel: taskmanager.ErrInvalidAgentResponseSentinel},
		{name: "extended card", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeAuthenticatedExtendedCardNotConfigured), sentinel: taskmanager.ErrAuthenticatedExtendedCardNotConfiguredSentinel},
		{name: "extension", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeExtensionSupportRequired), sentinel: taskmanager.ErrExtensionSupportRequiredSentinel},
		{name: "version", statusCode: http.StatusBadRequest, body: errorBody(taskmanager.ErrCodeVersionNotSupported), sentinel: taskmanager.ErrVersionNotSupportedSentinel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := decodeHTTPJSONError(tt.statusCode, []byte(tt.body))
			require.Error(t, err)
			if tt.sentinel != nil {
				assert.ErrorIs(t, err, tt.sentinel)
			}
		})
	}
}

// The server's message must reach the caller verbatim, and must not be recycled
// into a constructor that expects a specific value (a version, a content type).
func TestDecodeHTTPJSONErrorPreservesMessage(t *testing.T) {
	body := []byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION",` +
		`"message":"Requested A2A protocol version '0.5' is not supported by this agent",` +
		`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VERSION_NOT_SUPPORTED","domain":"a2a-protocol.org"}]}}`)
	err := decodeHTTPJSONError(http.StatusBadRequest, body)
	require.Error(t, err)
	assert.ErrorIs(t, err, taskmanager.ErrVersionNotSupportedSentinel)
	var taskErr *taskmanager.Error
	require.ErrorAs(t, err, &taskErr)
	assert.Equal(t, "Requested A2A protocol version '0.5' is not supported by this agent", taskErr.Message)
	assert.Contains(t, err.Error(), "'0.5'")

	// An unrecognized reason falls back on the HTTP status, keeping the message.
	body = []byte(`{"error":{"code":503,"status":"UNAVAILABLE","message":"upstream is down"}}`)
	err = decodeHTTPJSONError(http.StatusServiceUnavailable, body)
	require.ErrorAs(t, err, &taskErr)
	assert.Equal(t, taskmanager.ErrCodeInternalError, taskErr.Code)
	assert.Equal(t, "upstream is down", taskErr.Message)
}

// An empty path parameter must be rejected before it can address the collection
// resource: GET /tasks/ is ListTasks, whose body would decode into an empty Task.
func TestBuildHTTPJSONRequestRejectsEmptyIDs(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		params    any
	}{
		{"get task", protocol.MethodTasksGet, protocol.TaskQueryParams{}},
		{"cancel", protocol.MethodTasksCancel, protocol.TaskIDParams{}},
		{"subscribe", protocol.MethodTasksResubscribe, protocol.TaskIDParams{}},
		{"create push", protocol.MethodTasksPushNotificationConfigSet, protocol.TaskPushNotificationConfig{}},
		{"get push no task", protocol.MethodTasksPushNotificationConfigGet, protocol.GetTaskPushNotificationConfigParams{ID: "config"}},
		{"get push no config", protocol.MethodTasksPushNotificationConfigGet, protocol.GetTaskPushNotificationConfigParams{TaskID: "task"}},
		{"list push", protocol.MethodTasksPushNotificationConfigList, protocol.ListTaskPushNotificationConfigsParams{}},
		{"delete push no task", protocol.MethodTasksPushNotificationConfigDelete, protocol.DeleteTaskPushNotificationConfigParams{ID: "config"}},
		{"delete push no config", protocol.MethodTasksPushNotificationConfigDelete, protocol.DeleteTaskPushNotificationConfigParams{TaskID: "task"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildHTTPJSONRequest(tt.operation, tt.params)
			require.Error(t, err)
			assert.ErrorIs(t, err, taskmanager.ErrInvalidParamsSentinel)
		})
	}
}
