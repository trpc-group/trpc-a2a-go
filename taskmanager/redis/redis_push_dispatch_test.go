// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// recordingSender captures every delivery so a test can assert what the manager
// dispatched. It is safe for concurrent use: dispatch runs in the background.
type recordingSender struct {
	mu    sync.Mutex
	calls []recordedPush
}

type recordedPush struct {
	cfg   protocol.TaskPushNotificationConfig
	event protocol.StreamResponse
}

func (r *recordingSender) SendPush(
	_ context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedPush{cfg: cfg, event: event})
	return nil
}

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingSender) snapshot() []recordedPush {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedPush(nil), r.calls...)
}

// waitPushCount waits until the sender has recorded at least n deliveries.
func waitPushCount(t *testing.T, s *recordingSender, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("push count did not reach %d in time (got %d)", n, s.count())
}

func assertPushUnsupported(t *testing.T, err error) {
	t.Helper()
	var je *jsonrpc.Error
	if !errors.As(err, &je) || je.Code != taskmanager.ErrPushNotificationNotSupported().Code {
		t.Fatalf("want PushNotificationNotSupported, got %v", err)
	}
}

// TestRedisPushInlineConfigDelivered covers the full loop: a message carrying an
// inline push config registers the webhook, and the framework delivers the
// terminal event to it. The content-less working heartbeat is not delivered.
func TestRedisPushInlineConfigDelivered(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),                  // heartbeat: not pushed
		statusEvent(protocol.TaskStateCompleted, agentReply("done")), // pushed
	), WithPushNotifications(sender))
	defer m.Close()

	const webhook = "https://example.com/hook"
	params := sendParams("do work", "")
	params.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: webhook},
	}
	if _, err := m.OnSendMessage(context.Background(), params); err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}

	waitPushCount(t, sender, 1)
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 push (terminal only), got %d", len(calls))
	}
	su := calls[0].event.GetStatusUpdate()
	if su == nil || su.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("want Completed push, got %+v", calls[0].event)
	}
	if calls[0].cfg.URL != webhook {
		t.Fatalf("want webhook %q, got %q", webhook, calls[0].cfg.URL)
	}
}

// TestRedisPushRegisteredConfigDelivered covers the RPC registration path: a
// config set via OnPushNotificationSet is delivered when a later continuation
// round drives the task to a terminal state.
func TestRedisPushRegisteredConfigDelivered(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	), WithPushNotifications(sender))
	defer m.Close()

	task := storedTask(t, m, "task-reg", "ctx-reg", protocol.TaskStateWorking)
	const webhook = "https://example.com/reg"
	if _, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		URL:    webhook,
	}); err != nil {
		t.Fatalf("OnPushNotificationSet: %v", err)
	}

	// A continuation message targeting the task drives the Completed event.
	msg := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart("go")})
	cid := task.ContextID
	msg.TaskID = &task.ID
	msg.ContextID = &cid
	if _, err := m.OnSendMessage(context.Background(), protocol.SendMessageParams{Message: msg}); err != nil {
		t.Fatalf("OnSendMessage continuation: %v", err)
	}

	waitPushCount(t, sender, 1)
	calls := sender.snapshot()
	if su := calls[0].event.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("want Completed push, got %+v", calls[0].event)
	}
	if calls[0].cfg.URL != webhook {
		t.Fatalf("want webhook %q, got %q", webhook, calls[0].cfg.URL)
	}
}

// TestRedisPushRejectedWithoutSender verifies the -32003 gate: with no Sender
// configured, both the config RPC and an inline config are rejected.
func TestRedisPushRejectedWithoutSender(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	defer m.Close()

	task := storedTask(t, m, "task-nogate", "ctx-nogate", protocol.TaskStateWorking)
	_, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		URL:    "https://example.com/hook",
	})
	assertPushUnsupported(t, err)

	params := sendParams("hi", "")
	params.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/hook"},
	}
	_, err = m.OnSendMessage(context.Background(), params)
	assertPushUnsupported(t, err)
}

// TestRedisPushManualSuppresses verifies push.Config.ManualDelivery keeps
// registration working (config persisted, listable) while suppressing
// automatic delivery.
func TestRedisPushManualSuppresses(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	), WithPushNotificationsConfig(push.Config{Sender: sender, ManualDelivery: true}))
	defer m.Close()

	task := storedTask(t, m, "task-manual", "ctx-manual", protocol.TaskStateWorking)
	const webhook = "https://example.com/manual"
	if _, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		URL:    webhook,
	}); err != nil {
		t.Fatalf("OnPushNotificationSet under Manual: %v", err)
	}

	// A continuation drives the task to Completed; the framework would auto-push
	// here if the Sender were not Manual.
	msg := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart("go")})
	cid := task.ContextID
	msg.TaskID = &task.ID
	msg.ContextID = &cid
	if _, err := m.OnSendMessage(context.Background(), protocol.SendMessageParams{Message: msg}); err != nil {
		t.Fatalf("OnSendMessage continuation: %v", err)
	}
	waitTaskState(t, m, task.ID, protocol.TaskStateCompleted)

	// Registration went through (gate passed under Manual): the config is listable...
	list, err := m.OnPushNotificationList(context.Background(), protocol.ListTaskPushNotificationConfigsParams{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("OnPushNotificationList: %v", err)
	}
	if len(list.Configs) != 1 || list.Configs[0].URL != webhook {
		t.Fatalf("want the registered config listable, got %+v", list.Configs)
	}
	// ...but the Sender was never invoked: manual mode suppresses auto-delivery.
	if got := sender.count(); got != 0 {
		t.Fatalf("manual delivery mode must suppress automatic delivery, got %d deliveries", got)
	}
}

// TestRedisManualWithoutSenderFails pins the invalid config: manual delivery
// without a Sender is a construction error, not silence.
func TestRedisManualWithoutSenderFails(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	if _, err := NewTaskManager(scriptedExecutor(), client,
		WithPushNotificationsConfig(push.Config{ManualDelivery: true})); err == nil {
		t.Fatal("ManualDelivery without a Sender must fail construction")
	}
}

// TestRedisPushSenderAccessor verifies the accessor the server's discovery reads.
func TestRedisPushSenderAccessor(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(), WithPushNotifications(sender))
	defer m.Close()
	if m.PushSender() != push.Sender(sender) {
		t.Fatalf("PushSender() did not return the configured sender")
	}

	plain, _ := setupTest(t, scriptedExecutor())
	defer plain.Close()
	if plain.PushSender() != nil {
		t.Fatalf("PushSender() must be nil when push is not configured")
	}
}

// TestRedisPushWorthy pins the delivery policy: content-less working/submitted
// heartbeats are skipped; everything else is delivered.
func TestRedisPushWorthy(t *testing.T) {
	cases := []struct {
		name  string
		event protocol.StreamResponse
		want  bool
	}{
		{"working-heartbeat", protocol.NewStreamResponseStatusUpdate(statusEvent(protocol.TaskStateWorking, nil)), false},
		{"submitted-heartbeat", protocol.NewStreamResponseStatusUpdate(statusEvent(protocol.TaskStateSubmitted, nil)), false},
		{"working-with-message", protocol.NewStreamResponseStatusUpdate(statusEvent(protocol.TaskStateWorking, agentReply("x"))), true},
		{"completed", protocol.NewStreamResponseStatusUpdate(statusEvent(protocol.TaskStateCompleted, nil)), true},
		{"input-required", protocol.NewStreamResponseStatusUpdate(statusEvent(protocol.TaskStateInputRequired, nil)), true},
		{"message", protocol.NewStreamResponseMessage(agentReply("hi")), true},
	}
	for _, c := range cases {
		if got := pushWorthy(c.event); got != c.want {
			t.Errorf("%s: pushWorthy = %v, want %v", c.name, got, c.want)
		}
	}
}
