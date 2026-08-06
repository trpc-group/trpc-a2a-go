// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	a2aServer, err := server.NewA2AServer(manager, server.WithAgentCard(card))
	require.NoError(t, err)
	testServer := httptest.NewServer(a2aServer.Handler())
	t.Cleanup(testServer.Close)

	clientCard := card
	clientCard.SupportedInterfaces[0].URL = testServer.URL
	client, err := NewA2AClientFromAgentCard(&clientCard)
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

func TestNewA2AClientFromAgentCardSelectionAndTenant(t *testing.T) {
	card := &protocol.AgentCard{SupportedInterfaces: []protocol.AgentInterface{
		{URL: "https://unsupported.example", ProtocolBinding: "CUSTOM", ProtocolVersion: protocol.ProtocolVersionV1},
		{URL: "https://rest.example/api", ProtocolBinding: protocol.ProtocolBindingHTTPJSON, Tenant: "tenant-a", ProtocolVersion: protocol.ProtocolVersionV1},
		{URL: "https://rpc.example/rpc", ProtocolBinding: protocol.ProtocolBindingJSONRPC, ProtocolVersion: protocol.ProtocolVersionV1},
	}, Signatures: []protocol.AgentCardSignature{{Protected: "header", Signature: "signature"}}}
	before, err := json.Marshal(card)
	require.NoError(t, err)

	client, err := NewA2AClientFromAgentCard(card)
	require.NoError(t, err)
	assert.Equal(t, protocol.ProtocolBindingHTTPJSON, client.protocolBinding)
	assert.Equal(t, "tenant-a", client.tenant)
	assert.Equal(t, "https://rest.example/api/", client.baseURL.String())
	after, err := json.Marshal(card)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after), "client construction must not mutate a signed Agent Card")

	rpcClient, err := NewA2AClientFromAgentCard(card, WithProtocolBinding(protocol.ProtocolBindingJSONRPC))
	require.NoError(t, err)
	assert.Equal(t, protocol.ProtocolBindingJSONRPC, rpcClient.protocolBinding)
	assert.Equal(t, "https://rpc.example/rpc/", rpcClient.baseURL.String())

	_, err = client.SendMessage(context.Background(), protocol.SendMessageParams{
		Tenant:  "tenant-b",
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []*protocol.Part{protocol.NewTextPart("hello")}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match selected AgentInterface tenant")
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
		if r.URL.EscapedPath() != "/tenant%2Fa/message:send" {
			recordRequestErr(fmt.Errorf("path = %s", r.URL.EscapedPath()))
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
		w.Header().Set("Content-Type", protocol.MediaTypeA2AJSON)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": body["message"]})
	}))
	t.Cleanup(testServer.Close)

	client, err := NewA2AClient(testServer.URL, WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
	require.NoError(t, err)
	_, err = client.SendMessage(context.Background(), protocol.SendMessageParams{
		Tenant: "tenant/a",
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

func TestDecodeHTTPJSONError(t *testing.T) {
	body := []byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"missing","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TASK_NOT_FOUND","domain":"a2a-protocol.org","metadata":{"taskId":"task-1"}}]}}`)
	err := decodeHTTPJSONError(http.StatusNotFound, body)
	require.Error(t, err)
	assert.ErrorIs(t, err, taskmanager.ErrTaskNotFoundSentinel)

	body = []byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"not cancelable","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TASK_NOT_CANCELABLE","domain":"a2a-protocol.org","metadata":{"taskId":"task-1"}}]}}`)
	err = decodeHTTPJSONError(http.StatusBadRequest, body)
	require.Error(t, err)
	assert.ErrorIs(t, err, taskmanager.ErrTaskNotCancelableSentinel)
}
