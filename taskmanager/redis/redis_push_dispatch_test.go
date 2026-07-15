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
// inline push config registers the webhook, and the framework delivers every
// task event to it in order.
func TestRedisPushInlineConfigDelivered(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	), WithPushNotifications(push.Config{Sender: sender}))
	defer m.Close()

	const webhook = "https://example.com/hook"
	params := sendParams("do work", "")
	params.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: webhook},
	}
	if _, err := m.OnSendMessage(context.Background(), params); err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}

	waitPushCount(t, sender, 2)
	calls := sender.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want both status updates delivered, got %d", len(calls))
	}
	for i, want := range []protocol.TaskState{protocol.TaskStateWorking, protocol.TaskStateCompleted} {
		su := calls[i].event.GetStatusUpdate()
		if su == nil || su.Status.State != want {
			t.Fatalf("push %d state = %+v, want %s", i, calls[i].event, want)
		}
	}
	if calls[0].cfg.URL != webhook {
		t.Fatalf("want webhook %q, got %q", webhook, calls[0].cfg.URL)
	}
}

func TestRedisInlinePushConfigMessageOnlyLeavesNoOrphan(t *testing.T) {
	taskIDs := make(chan string, 1)
	processor := executorFunc(func(
		_ context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		taskIDs <- ec.TaskID
		out := make(chan protocol.StreamEvent, 1)
		out <- agentReply("message only")
		close(out)
		return out, nil
	})
	m, mr := setupTest(t, processor,
		WithPushNotifications(push.Config{ManualDelivery: true}))
	defer m.Close()
	params := sendParams("hello", "")
	params.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/hook"},
	}
	if _, err := m.OnSendMessage(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	taskID := <-taskIDs
	if mr.Exists(taskPrefix+taskID) || mr.Exists(pushNotificationPrefix+taskID) {
		t.Fatalf("message-only round created task or push-config keys for %s", taskID)
	}
	list, err := m.OnPushNotificationList(context.Background(),
		protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) || list != nil {
		t.Fatalf("message-only round left a queryable config: list=%+v err=%v", list, err)
	}
}

func TestRedisManualInlinePushConfigPersists(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	), WithPushNotifications(push.Config{ManualDelivery: true}))
	defer m.Close()
	params := sendParams("hello", "")
	params.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/hook"},
	}
	resp, err := m.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	task := resp.GetTask()
	if task == nil {
		t.Fatalf("OnSendMessage response = %+v, want task", resp)
	}
	list, err := m.OnPushNotificationList(context.Background(),
		protocol.ListTaskPushNotificationConfigsParams{TaskID: task.ID})
	if err != nil || len(list.Configs) != 1 || list.Configs[0].TaskID != task.ID {
		t.Fatalf("manual inline config: list=%+v err=%v", list, err)
	}
}

// TestRedisPushRegisteredConfigDelivered covers the RPC registration path:
// every config registered for a task is delivered when a later continuation
// round drives the task to a terminal state.
func TestRedisPushRegisteredConfigDelivered(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	), WithPushNotifications(push.Config{Sender: sender}))
	defer m.Close()

	task := storedTask(t, m, "task-reg", "ctx-reg", protocol.TaskStateWorking)
	const webhook = "https://example.com/reg"
	if _, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		ID:     "primary",
		URL:    webhook,
	}); err != nil {
		t.Fatalf("OnPushNotificationSet: %v", err)
	}
	const secondWebhook = "https://example.com/reg-secondary"
	if _, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		ID:     "secondary",
		URL:    secondWebhook,
	}); err != nil {
		t.Fatalf("OnPushNotificationSet second config: %v", err)
	}

	// A continuation message targeting the task drives the Completed event.
	msg := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart("go")})
	cid := task.ContextID
	msg.TaskID = &task.ID
	msg.ContextID = &cid
	if _, err := m.OnSendMessage(context.Background(), protocol.SendMessageParams{Message: msg}); err != nil {
		t.Fatalf("OnSendMessage continuation: %v", err)
	}

	waitPushCount(t, sender, 2)
	calls := sender.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want one delivery per registered config, got %d", len(calls))
	}
	gotURLs := map[string]bool{}
	for _, call := range calls {
		if su := call.event.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCompleted {
			t.Fatalf("want Completed push, got %+v", call.event)
		}
		gotURLs[call.cfg.URL] = true
	}
	if !gotURLs[webhook] || !gotURLs[secondWebhook] {
		t.Fatalf("deliveries did not cover both configs: %+v", gotURLs)
	}
}

// TestRedisPushRejectedWhenDisabled verifies the -32003 gate when neither
// automatic nor manual delivery is enabled.
func TestRedisPushRejectedWhenDisabled(t *testing.T) {
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
	), WithPushNotifications(push.Config{Sender: sender, ManualDelivery: true}))
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

// TestRedisManualWithoutSenderAllowsRegistration pins the separation between
// push capability and automatic transport.
func TestRedisManualWithoutSenderAllowsRegistration(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(),
		WithPushNotifications(push.Config{ManualDelivery: true}))
	defer m.Close()
	task := storedTask(t, m, "manual-without-sender", "ctx-manual-without-sender", protocol.TaskStateWorking)
	ctx := context.Background()
	stored, err := m.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		URL:    "https://example.com/hook",
	})
	if err != nil {
		t.Fatalf("manual registration without Sender: %v", err)
	}
	if _, err := m.OnPushNotificationGet(ctx, protocol.GetTaskPushNotificationConfigParams{
		TaskID: stored.TaskID, ID: stored.ID,
	}); err != nil {
		t.Fatalf("manual Get without Sender: %v", err)
	}
	list, err := m.OnPushNotificationList(ctx,
		protocol.ListTaskPushNotificationConfigsParams{TaskID: stored.TaskID})
	if err != nil || len(list.Configs) != 1 {
		t.Fatalf("manual List without Sender: list=%+v err=%v", list, err)
	}
	if err := m.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{
		TaskID: stored.TaskID, ID: stored.ID,
	}); err != nil {
		t.Fatalf("manual Delete without Sender: %v", err)
	}
	if !m.SupportsPushNotifications() {
		t.Fatal("manual delivery must advertise push support")
	}
}

// TestRedisSupportsPushNotifications verifies the capability the server reads.
func TestRedisSupportsPushNotifications(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(), WithPushNotifications(push.Config{Sender: sender}))
	defer m.Close()
	if !m.SupportsPushNotifications() {
		t.Fatalf("SupportsPushNotifications() = false with a configured sender")
	}

	plain, _ := setupTest(t, scriptedExecutor())
	defer plain.Close()
	if plain.SupportsPushNotifications() {
		t.Fatalf("SupportsPushNotifications() = true without a sender or manual delivery")
	}
}
