// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/stateless"
)

// httpJSONTaskProcessor runs a task to completion so the retaining manager has
// something for the task-scoped operations to address.
type httpJSONTaskProcessor struct{}

func (httpJSONTaskProcessor) ProcessMessage(
	_ context.Context,
	_ *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	events := make(chan protocol.StreamEvent, 2)
	events <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
	events <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateCompleted}}
	close(events)
	return events, nil
}

// newHTTPJSONFixture serves one agent over both bindings and returns a client
// for each, so every assertion below can be made about the pair.
func newHTTPJSONFixture(t *testing.T) (rest, rpc *A2AClient) {
	t.Helper()
	manager, err := memory.NewTaskManager(
		httpJSONTaskProcessor{},
		memory.WithPushNotifications(push.Config{ManualDelivery: true}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	enabled := true
	card := protocol.AgentCard{
		Name:        "HTTP+JSON Agent",
		Description: "test",
		Version:     "1",
		Capabilities: protocol.AgentCapabilities{
			Streaming:         &enabled,
			PushNotifications: &enabled,
			ExtendedAgentCard: &enabled,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []protocol.AgentSkill{},
	}
	a2aServer, err := server.NewA2AServer(manager,
		server.WithAgentCard(card), server.WithHTTPJSONEndpoint("/"))
	require.NoError(t, err)
	testServer := httptest.NewServer(a2aServer.Handler())
	t.Cleanup(testServer.Close)

	rest, err = NewA2AClient(testServer.URL, WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
	require.NoError(t, err)
	rpc, err = NewA2AClient(testServer.URL)
	require.NoError(t, err)
	return rest, rpc
}

func newHTTPJSONTask(t *testing.T, c *A2AClient, messageID string) string {
	t.Helper()
	response, err := c.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: protocol.Message{
			MessageID: messageID,
			Role:      protocol.MessageRoleUser,
			Parts:     []*protocol.Part{protocol.NewTextPart("go")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response.GetTask())
	return response.GetTask().ID
}

// The task-scoped and push-config operations must round-trip over the REST
// binding, not just message:send and message:stream.
func TestHTTPJSONEndToEndOperations(t *testing.T) {
	rest, _ := newHTTPJSONFixture(t)
	ctx := context.Background()
	taskID := newHTTPJSONTask(t, rest, "message-1")

	task, err := rest.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})
	require.NoError(t, err)
	assert.Equal(t, taskID, task.ID)
	assert.Equal(t, protocol.TaskStateCompleted, task.Status.State)

	list, err := rest.ListTasks(ctx, protocol.ListTasksParams{})
	require.NoError(t, err)
	require.Len(t, list.Tasks, 1)
	assert.Equal(t, taskID, list.Tasks[0].ID)

	created, err := rest.SetPushNotification(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, ID: "config-1", URL: "https://example.com/hook",
	})
	require.NoError(t, err)
	assert.Equal(t, "config-1", created.ID)

	got, err := rest.GetPushNotification(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: taskID, ID: "config-1"})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/hook", got.URL)

	configs, err := rest.ListPushNotifications(ctx,
		protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	require.NoError(t, err)
	require.Len(t, configs.Configs, 1)

	require.NoError(t, rest.DeletePushNotification(ctx,
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: taskID, ID: "config-1"}))
	configs, err = rest.ListPushNotifications(ctx,
		protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	require.NoError(t, err)
	assert.Empty(t, configs.Configs)

	extended, err := rest.GetAuthenticatedExtendedCard(ctx)
	require.NoError(t, err)
	assert.Equal(t, "HTTP+JSON Agent", extended.Name)

	// A completed task is no longer cancelable, and the semantic error must
	// survive the google.rpc.Status round trip.
	_, err = rest.CancelTasks(ctx, protocol.TaskIDParams{ID: taskID})
	assert.ErrorIs(t, err, taskmanager.ErrTaskNotCancelableSentinel)

	_, err = rest.GetTasks(ctx, protocol.TaskQueryParams{ID: "no-such-task"})
	assert.ErrorIs(t, err, taskmanager.ErrTaskNotFoundSentinel)
}

// Both bindings must reject the same request with the same semantic error and
// the same diagnostic detail (spec §5.1, "Same Error Handling").
func TestHTTPJSONErrorParityWithJSONRPC(t *testing.T) {
	rest, rpc := newHTTPJSONFixture(t)
	ctx := context.Background()

	bad := protocol.SendMessageParams{Message: protocol.Message{
		MessageID: "message-bad",
		Role:      protocol.MessageRoleAgent,
		Parts:     []*protocol.Part{protocol.NewTextPart("hi")},
	}}
	_, restErr := rest.SendMessage(ctx, bad)
	require.Error(t, restErr)
	assert.ErrorIs(t, restErr, taskmanager.ErrInvalidParamsSentinel)
	assert.Contains(t, restErr.Error(), "message role must be ROLE_USER")

	_, rpcErr := rpc.SendMessage(ctx, bad)
	require.Error(t, rpcErr)
	assert.Contains(t, rpcErr.Error(), "message role must be ROLE_USER")

	// An empty task ID is a client-side error, not a request that quietly
	// addresses the task collection and decodes its answer as an empty Task.
	task, err := rest.GetTasks(ctx, protocol.TaskQueryParams{ID: ""})
	require.Error(t, err)
	assert.Nil(t, task)
	assert.ErrorIs(t, err, taskmanager.ErrInvalidParamsSentinel)

	config, err := rest.GetPushNotification(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: newHTTPJSONTask(t, rest, "message-2")})
	require.Error(t, err)
	assert.Nil(t, config)
	assert.ErrorIs(t, err, taskmanager.ErrInvalidParamsSentinel)
}

// A multi-tenant agent is addressed through the {tenant} path segment the
// normative proto binds for every operation, which is also what the reference
// clients put on the wire. POST bodies carry the tenant field as well.
func TestHTTPJSONTenantIsAPathSegment(t *testing.T) {
	spec, err := buildHTTPJSONRequest(protocol.MethodTasksGet, protocol.TaskQueryParams{ID: "task-1", Tenant: "tenant-a"})
	require.NoError(t, err)
	assert.Equal(t, "/tenant-a/tasks/task-1", spec.path)
	assert.Empty(t, spec.query.Get("tenant"))

	spec, err = buildHTTPJSONRequest(protocol.MethodMessageSend, protocol.SendMessageParams{Tenant: "tenant-a"})
	require.NoError(t, err)
	assert.Equal(t, "/tenant-a/message:send", spec.path)
	sent, ok := spec.body.(protocol.SendMessageParams)
	require.True(t, ok)
	assert.Equal(t, "tenant-a", sent.Tenant)
}

func TestConfiguredTenantPropagatesToExtendedCard(t *testing.T) {
	manager, err := stateless.NewTaskManager(httpJSONEchoProcessor{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	extended := true
	card := protocol.AgentCard{
		Name:               "tenant card",
		Description:        "test",
		Version:            "1",
		Capabilities:       protocol.AgentCapabilities{ExtendedAgentCard: &extended},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []protocol.AgentSkill{},
		SupportedInterfaces: []protocol.AgentInterface{
			{URL: "http://placeholder.invalid", ProtocolBinding: protocol.ProtocolBindingJSONRPC, ProtocolVersion: protocol.ProtocolVersionV1},
			{URL: "http://placeholder.invalid", ProtocolBinding: protocol.ProtocolBindingHTTPJSON, ProtocolVersion: protocol.ProtocolVersionV1},
		},
	}
	srv, err := server.NewA2AServer(
		manager,
		server.WithTenantCard("tasks", card),
		server.WithHTTPJSONEndpoint("/"),
		server.WithAuthenticatedExtendedCardHandler(func(_ context.Context, base server.AgentCard) (server.AgentCard, error) {
			base.Name = "extended tasks tenant"
			return base, nil
		}),
	)
	require.NoError(t, err)
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)

	for _, binding := range []string{protocol.ProtocolBindingJSONRPC, protocol.ProtocolBindingHTTPJSON} {
		t.Run(binding, func(t *testing.T) {
			client, err := NewA2AClient(testServer.URL, WithProtocolBinding(binding), WithTenant("tasks"))
			require.NoError(t, err)
			extendedCard, err := client.GetAuthenticatedExtendedCard(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "extended tasks tenant", extendedCard.Name)
		})
	}
}

// SubscribeToTask retries with GET when the server rejects POST: the v1.0
// specification text uses POST and the normative proto binds GET.
func TestHTTPJSONSubscribeFallsBackToGET(t *testing.T) {
	var methods []string
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPost {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", protocol.MediaTypeEventStream)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(testServer.Close)

	c, err := NewA2AClient(testServer.URL, WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
	require.NoError(t, err)
	events, err := c.ResubscribeTask(context.Background(), protocol.TaskIDParams{ID: "task-1"})
	require.NoError(t, err)
	for range events {
	}
	assert.Equal(t, []string{http.MethodPost, http.MethodGet}, methods)
}

// A task ID containing a colon must stay addressable: the client escapes it so
// the server does not read it as an operation verb.
func TestHTTPJSONColonInTaskIDRoundTrips(t *testing.T) {
	var seen []string
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", protocol.MediaTypeA2AJSON)
		_, _ = w.Write([]byte(`{"id":"job:cancel","contextId":"ctx","status":{"state":"TASK_STATE_COMPLETED"}}`))
	}))
	t.Cleanup(testServer.Close)

	c, err := NewA2AClient(testServer.URL, WithProtocolBinding(protocol.ProtocolBindingHTTPJSON))
	require.NoError(t, err)
	task, err := c.GetTasks(context.Background(), protocol.TaskQueryParams{ID: "job:cancel"})
	require.NoError(t, err)
	assert.Equal(t, "job:cancel", task.ID)
	// The colon is percent-encoded, so the server reads it as part of the ID
	// rather than as the ":cancel" verb (see TestParseHTTPJSONRoute).
	assert.Equal(t, []string{"GET /tasks/job%3Acancel"}, seen)
}
