// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// fakeTaskManager is a minimal v1 TaskManager that records the converted
// request and returns canned v1 responses, so the tests exercise exactly the
// compat translation.
type fakeTaskManager struct {
	gotSendParams *protocol.SendMessageParams
	sendResponse  *protocol.SendMessageResponse
	streamEvents  []protocol.StreamResponse
	task          *protocol.Task
	pushConfig    *protocol.TaskPushNotificationConfig
}

func (f *fakeTaskManager) OnSendMessage(
	ctx context.Context, p protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	f.gotSendParams = &p
	return f.sendResponse, nil
}

func (f *fakeTaskManager) OnSendMessageStream(
	ctx context.Context, p protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	f.gotSendParams = &p
	ch := make(chan protocol.StreamResponse, len(f.streamEvents))
	for _, ev := range f.streamEvents {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (f *fakeTaskManager) OnGetTask(
	ctx context.Context, p protocol.TaskQueryParams,
) (*protocol.Task, error) {
	return f.task, nil
}

func (f *fakeTaskManager) OnCancelTask(
	ctx context.Context, p protocol.TaskIDParams,
) (*protocol.Task, error) {
	return f.task, nil
}

func (f *fakeTaskManager) OnListTasks(
	ctx context.Context, p protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	return &protocol.ListTasksResult{}, nil
}

func (f *fakeTaskManager) OnPushNotificationSet(
	ctx context.Context, p protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	f.pushConfig = &p
	return &p, nil
}

func (f *fakeTaskManager) OnPushNotificationGet(
	ctx context.Context, p protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	return f.pushConfig, nil
}

func (f *fakeTaskManager) OnPushNotificationList(
	ctx context.Context, p protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	return &protocol.ListTaskPushNotificationConfigsResult{}, nil
}

func (f *fakeTaskManager) OnPushNotificationDelete(
	ctx context.Context, p protocol.DeleteTaskPushNotificationConfigParams,
) error {
	return nil
}

func (f *fakeTaskManager) OnResubscribe(
	ctx context.Context, p protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	ch := make(chan protocol.StreamResponse, len(f.streamEvents))
	for _, ev := range f.streamEvents {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func postJSON(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestHandler_MessageSend_LegacyWire sends a real v0.2.5 wire request and
// asserts both the inbound conversion (what the v1 task manager sees) and the
// outbound legacy wire shape.
func TestHandler_MessageSend_LegacyWire(t *testing.T) {
	agentMsg := protocol.NewMessage(protocol.MessageRoleAgent,
		[]*protocol.Part{protocol.NewTextPart("hi from v1 core")})
	fake := &fakeTaskManager{
		sendResponse: protocol.NewSendMessageResponseMessage(&agentMsg),
	}
	h := NewJSONRPCHandler(fake)

	// Verbatim legacy wire format: kind discriminators, lowercase role,
	// blocking flag.
	w := postJSON(t, h, `{
		"jsonrpc": "2.0", "id": "req-1", "method": "message/send",
		"params": {
			"message": {
				"kind": "message", "messageId": "m1", "role": "user",
				"parts": [{"kind": "text", "text": "hello"}]
			},
			"configuration": {"blocking": true, "acceptedOutputModes": ["text"]}
		}
	}`)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// Inbound: the v1 core must have received translated types.
	require.NotNil(t, fake.gotSendParams)
	assert.Equal(t, protocol.MessageRoleUser, fake.gotSendParams.Message.Role)
	assert.Equal(t, "hello", fake.gotSendParams.Message.Parts[0].TextContent())
	require.NotNil(t, fake.gotSendParams.Configuration)
	assert.True(t, fake.gotSendParams.Configuration.IsBlocking())

	// Outbound: legacy wire shape (kind + lowercase role, no v1 wrapper).
	var resp struct {
		Result map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "message", resp.Result["kind"])
	assert.Equal(t, "agent", resp.Result["role"])
	parts := resp.Result["parts"].([]interface{})
	part := parts[0].(map[string]interface{})
	assert.Equal(t, "text", part["kind"])
	assert.Equal(t, "hi from v1 core", part["text"])
}

func TestHandler_TasksGet_LegacyWire(t *testing.T) {
	fake := &fakeTaskManager{
		task: &protocol.Task{
			ID:        "task-1",
			ContextID: "ctx-1",
			Status:    protocol.TaskStatus{State: protocol.TaskStateWorking},
		},
	}
	h := NewJSONRPCHandler(fake)

	w := postJSON(t, h, `{"jsonrpc":"2.0","id":"req-2","method":"tasks/get","params":{"id":"task-1"}}`)
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.Contains(t, body, `"kind":"task"`)
	assert.Contains(t, body, `"state":"working"`, "legacy lowercase enum expected, body: %s", body)
	assert.NotContains(t, body, "TASK_STATE_", "v1 enums must not leak to the legacy wire")
}

func TestHandler_Streaming_LegacyWire(t *testing.T) {
	fake := &fakeTaskManager{
		streamEvents: []protocol.StreamResponse{
			{StatusUpdate: &protocol.TaskStatusUpdateEvent{
				TaskID: "task-1", ContextID: "ctx-1",
				Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
			}},
			{StatusUpdate: &protocol.TaskStatusUpdateEvent{
				TaskID: "task-1", ContextID: "ctx-1",
				Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
			}},
		},
	}
	h := NewJSONRPCHandler(fake)

	w := postJSON(t, h, `{
		"jsonrpc": "2.0", "id": "req-3", "method": "message/stream",
		"params": {"message": {"kind": "message", "messageId": "m1", "role": "user",
			"parts": [{"kind": "text", "text": "go"}]}}
	}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))

	reader := sse.NewEventReader(w.Body)
	var events []string
	var payloads []map[string]interface{}
	for {
		data, eventType, err := reader.ReadEvent()
		if err != nil {
			break
		}
		if len(data) == 0 {
			continue
		}
		events = append(events, eventType)
		var rpcResp struct {
			Result map[string]interface{} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(data, &rpcResp))
		payloads = append(payloads, rpcResp.Result)
	}

	require.Len(t, events, 2)
	assert.Equal(t, EventStatusUpdate, events[0], "legacy SSE event name expected")
	// Legacy kind discriminator and lowercase states on the wire.
	assert.Equal(t, "status-update", payloads[0]["kind"])
	status := payloads[0]["status"].(map[string]interface{})
	assert.Equal(t, "working", status["state"])
	assert.Equal(t, false, payloads[0]["final"])

	// Terminal event must carry the derived final=true flag.
	finalStatus := payloads[1]["status"].(map[string]interface{})
	assert.Equal(t, "completed", finalStatus["state"])
	assert.Equal(t, true, payloads[1]["final"])
}

func TestHandler_PushConfig_LegacyWire(t *testing.T) {
	fake := &fakeTaskManager{}
	h := NewJSONRPCHandler(fake)

	// Legacy nested shape with schemes list.
	w := postJSON(t, h, `{
		"jsonrpc": "2.0", "id": "req-4", "method": "tasks/pushNotificationConfig/set",
		"params": {
			"taskId": "task-1",
			"pushNotificationConfig": {
				"url": "https://example.com/hook",
				"token": "tok",
				"authentication": {"schemes": ["Bearer"], "credentials": "secret"}
			}
		}
	}`)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// The v1 core received the flat shape.
	require.NotNil(t, fake.pushConfig)
	assert.Equal(t, "task-1", fake.pushConfig.TaskID)
	assert.Equal(t, "https://example.com/hook", fake.pushConfig.URL)
	require.NotNil(t, fake.pushConfig.Authentication)
	assert.Equal(t, "Bearer", fake.pushConfig.Authentication.Scheme)

	// The legacy client received the nested shape back.
	body := w.Body.String()
	assert.Contains(t, body, `"pushNotificationConfig"`)
	assert.Contains(t, body, `"schemes":["Bearer"]`)
}

func TestHandler_MethodNotFound(t *testing.T) {
	h := NewJSONRPCHandler(&fakeTaskManager{})
	w := postJSON(t, h, `{"jsonrpc":"2.0","id":"x","method":"tasks/unknown","params":{}}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not supported")
}

func TestDualHandler_Routing(t *testing.T) {
	v1Hits := 0
	v1Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v1Hits++
		// Echo the method back so the test can confirm body restoration.
		body, _ := json.Marshal(map[string]string{"served": "v1"})
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "" && r.Method == http.MethodPost {
			t.Error("v1 handler received an unreadable body: dual dispatch must restore it")
		}
		_, _ = w.Write(body)
	})
	fake := &fakeTaskManager{
		task: &protocol.Task{ID: "t1", Status: protocol.TaskStatus{State: protocol.TaskStateWorking}},
	}
	dual := NewDualHandler(v1Handler, NewJSONRPCHandler(fake))

	// Legacy slash method goes to the v0 handler.
	w := postJSON(t, dual, `{"jsonrpc":"2.0","id":"1","method":"tasks/get","params":{"id":"t1"}}`)
	assert.Contains(t, w.Body.String(), `"kind":"task"`)
	assert.Equal(t, 0, v1Hits)

	// PascalCase method falls through to v1 with an intact body.
	w = postJSON(t, dual, `{"jsonrpc":"2.0","id":"2","method":"GetTask","params":{"id":"t1"}}`)
	assert.Contains(t, w.Body.String(), `"served":"v1"`)
	assert.Equal(t, 1, v1Hits)

	// GET requests (e.g. agent card) fall through to v1.
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	dual.ServeHTTP(rec, req)
	assert.Equal(t, 2, v1Hits)
}

func TestHandler_ExtendedCard(t *testing.T) {
	// Without the option the method is not found.
	h := NewJSONRPCHandler(&fakeTaskManager{})
	w := postJSON(t, h, `{"jsonrpc":"2.0","id":"1","method":"agent/getAuthenticatedExtendedCard"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// With the option, the card is served with legacy fields filled.
	card := &protocol.AgentCard{
		Name: "agent",
		SupportedInterfaces: []protocol.AgentInterface{
			{URL: "https://a.example.com", ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0"},
		},
	}
	h = NewJSONRPCHandler(&fakeTaskManager{}, WithExtendedAgentCard(
		func(ctx context.Context) (*protocol.AgentCard, error) { return card, nil },
	))
	w = postJSON(t, h, `{"jsonrpc":"2.0","id":"1","method":"agent/getAuthenticatedExtendedCard"}`)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"url":"https://a.example.com"`)
	assert.True(t, strings.Contains(body, `"protocolVersion":"0.2.5"`), "body: %s", body)
}
