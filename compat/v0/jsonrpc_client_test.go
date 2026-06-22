// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// TestClientServerLoopback proves the full legacy round trip over real HTTP:
// v1 params -> compat client (v0 wire) -> compat handler -> v1 task manager,
// and the response back through both translations. Both compat directions are
// exercised in a single hop.
func TestClientServerLoopback(t *testing.T) {
	agentMsg := protocol.NewMessage(protocol.MessageRoleAgent,
		[]*protocol.Part{protocol.NewTextPart("pong")})
	fake := &fakeTaskManager{
		sendResponse: protocol.NewSendMessageResponseMessage(&agentMsg),
		task: &protocol.Task{
			ID:        "task-1",
			ContextID: "ctx-1",
			Status:    protocol.TaskStatus{State: protocol.TaskStateCompleted},
		},
	}
	server := httptest.NewServer(NewJSONRPCHandler(fake))
	defer server.Close()

	client, err := NewClient(server.URL)
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("SendMessage", func(t *testing.T) {
		userMsg := protocol.NewMessage(protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart("ping")})
		resp, err := client.SendMessage(ctx, protocol.SendMessageParams{Message: userMsg})
		require.NoError(t, err)

		// The fake v1 core must have seen v1 types despite the v0 wire.
		require.NotNil(t, fake.gotSendParams)
		assert.Equal(t, protocol.MessageRoleUser, fake.gotSendParams.Message.Role)
		assert.Equal(t, "ping", fake.gotSendParams.Message.Parts[0].TextContent())

		// And the caller gets v1 types back.
		require.NotNil(t, resp.Message)
		assert.Equal(t, protocol.MessageRoleAgent, resp.Message.Role)
		assert.Equal(t, "pong", resp.Message.Parts[0].TextContent())
	})

	t.Run("GetTasks", func(t *testing.T) {
		task, err := client.GetTasks(ctx, protocol.TaskQueryParams{ID: "task-1"})
		require.NoError(t, err)
		assert.Equal(t, "task-1", task.ID)
		assert.Equal(t, protocol.TaskStateCompleted, task.Status.State)
	})

	t.Run("PushNotification", func(t *testing.T) {
		cfg, err := client.SetPushNotification(ctx, protocol.TaskPushNotificationConfig{
			TaskID: "task-1",
			URL:    "https://example.com/hook",
			Authentication: &protocol.AuthenticationInfo{
				Scheme: "Bearer", Credentials: "secret",
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "task-1", cfg.TaskID)
		assert.Equal(t, "https://example.com/hook", cfg.URL)
		require.NotNil(t, cfg.Authentication)
		assert.Equal(t, "Bearer", cfg.Authentication.Scheme)
		assert.Equal(t, "secret", cfg.Authentication.Credentials)
	})

	t.Run("StreamMessage", func(t *testing.T) {
		fake.streamEvents = []protocol.StreamResponse{
			{StatusUpdate: &protocol.TaskStatusUpdateEvent{
				TaskID: "task-1", ContextID: "ctx-1",
				Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
			}},
			{StatusUpdate: &protocol.TaskStatusUpdateEvent{
				TaskID: "task-1", ContextID: "ctx-1",
				Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
			}},
		}
		userMsg := protocol.NewMessage(protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart("go")})
		streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		events, err := client.StreamMessage(streamCtx, protocol.SendMessageParams{Message: userMsg})
		require.NoError(t, err)

		var received []protocol.StreamResponse
		for ev := range events {
			received = append(received, ev)
		}
		require.Len(t, received, 2)
		assert.Equal(t, protocol.TaskStateWorking, received[0].StatusUpdate.Status.State)
		assert.Equal(t, protocol.TaskStateCompleted, received[1].StatusUpdate.Status.State)
		assert.True(t, received[1].IsFinal())
	})
}

// TestClientServerLoopback_MoreMethods covers the remaining legacy methods over
// the real HTTP loopback (cancel, push get, resubscribe).
func TestClientServerLoopback_MoreMethods(t *testing.T) {
	fake := &fakeTaskManager{
		task: &protocol.Task{
			ID: "task-1", ContextID: "ctx-1",
			Status: protocol.TaskStatus{State: protocol.TaskStateCanceled},
		},
		pushConfig: &protocol.TaskPushNotificationConfig{
			TaskID: "task-1", URL: "https://example.com/hook",
		},
		streamEvents: []protocol.StreamResponse{
			{StatusUpdate: &protocol.TaskStatusUpdateEvent{
				TaskID: "task-1", Status: protocol.TaskStatus{State: protocol.TaskStateCompleted}}},
		},
	}
	server := httptest.NewServer(NewJSONRPCHandler(fake))
	defer server.Close()
	client, err := NewClient(server.URL)
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("CancelTasks", func(t *testing.T) {
		task, err := client.CancelTasks(ctx, protocol.TaskIDParams{ID: "task-1"})
		require.NoError(t, err)
		assert.Equal(t, protocol.TaskStateCanceled, task.Status.State)
	})

	t.Run("GetPushNotification", func(t *testing.T) {
		cfg, err := client.GetPushNotification(ctx, protocol.TaskIDParams{ID: "task-1"})
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/hook", cfg.URL)
	})

	t.Run("ResubscribeTask", func(t *testing.T) {
		events, err := client.ResubscribeTask(ctx, protocol.TaskIDParams{ID: "task-1"})
		require.NoError(t, err)
		var got []protocol.StreamResponse
		for ev := range events {
			got = append(got, ev)
		}
		require.Len(t, got, 1)
		assert.True(t, got[0].IsFinal())
	})
}
