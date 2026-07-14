// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// noopSender enables push (unlocking config registration) without delivering
// anything — the SenderFunc-based "agent delivers on its own schedule" mode.
func noopSender() push.Sender {
	return push.SenderFunc(func(context.Context, protocol.TaskPushNotificationConfig, protocol.StreamResponse) error {
		return nil
	})
}

// TestTaskManager_PushDispatchOnTerminalState verifies the end-to-end path: with
// a Sender configured, a task reaching a terminal state delivers a spec-shaped
// StreamResponse to every registered webhook — with no live SSE subscriber.
func TestTaskManager_PushDispatchOnTerminalState(t *testing.T) {
	received := make(chan []byte, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case received <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.NewHTTPSender()))

	taskID := "push-e2e-task"
	seedTask(manager, protocol.Task{
		ID:     taskID,
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})

	if _, err := manager.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: taskID,
		URL:    ts.URL,
	}); err != nil {
		t.Fatalf("OnPushNotificationSet: %v", err)
	}

	// Cancel drives a terminal status update, which must be pushed to the webhook.
	if _, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID}); err != nil {
		t.Fatalf("OnCancelTask: %v", err)
	}

	select {
	case body := <-received:
		var rr protocol.StreamResponse
		if err := json.Unmarshal(body, &rr); err != nil {
			t.Fatalf("webhook body is not a StreamResponse: %v; body=%s", err, body)
		}
		su := rr.GetStatusUpdate()
		if su == nil || su.Status.State != protocol.TaskStateCanceled {
			t.Errorf("expected a canceled statusUpdate, got %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook did not receive a push notification")
	}
}

// TestTaskManager_PushRejectedWithoutSender: with no Sender configured, push is
// not supported — config registration is rejected with -32003 instead of being
// stored and never delivered (a silent lie to the client).
func TestTaskManager_PushRejectedWithoutSender(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	ctx := context.Background()

	_, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: "no-sender-task", URL: "https://example.com/webhook",
	})
	if !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Fatalf("expected PushNotificationNotSupported, got %v", err)
	}
	if _, err := manager.OnPushNotificationList(ctx,
		protocol.ListTaskPushNotificationConfigsParams{TaskID: "no-sender-task"}); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Fatalf("expected PushNotificationNotSupported from List, got %v", err)
	}
	if _, err := manager.OnPushNotificationGet(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: "no-sender-task"}); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Fatalf("expected PushNotificationNotSupported from Get, got %v", err)
	}
	if err := manager.OnPushNotificationDelete(ctx,
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: "no-sender-task"}); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Fatalf("expected PushNotificationNotSupported from Delete, got %v", err)
	}
}

// TestPushWorthy checks which events are delivered vs suppressed. Content-less
// working/submitted heartbeats are skipped, but a working update that carries a
// message payload must still be delivered.
func TestPushWorthy(t *testing.T) {
	statusUpdate := func(state protocol.TaskState, msg *protocol.Message) protocol.StreamResponse {
		return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{State: state, Message: msg},
		})
	}
	msg := &protocol.Message{}
	cases := []struct {
		name  string
		event protocol.StreamResponse
		want  bool
	}{
		{"working heartbeat skipped", statusUpdate(protocol.TaskStateWorking, nil), false},
		{"submitted heartbeat skipped", statusUpdate(protocol.TaskStateSubmitted, nil), false},
		{"working with message delivered", statusUpdate(protocol.TaskStateWorking, msg), true},
		{"completed delivered", statusUpdate(protocol.TaskStateCompleted, nil), true},
		{"input-required delivered", statusUpdate(protocol.TaskStateInputRequired, nil), true},
		{"auth-required delivered", statusUpdate(protocol.TaskStateAuthRequired, nil), true},
		{"artifact delivered", protocol.NewStreamResponseArtifactUpdate(&protocol.TaskArtifactUpdateEvent{}), true},
		{"task delivered", protocol.NewStreamResponseTask(&protocol.Task{ID: "t1"}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pushWorthy(c.event); got != c.want {
				t.Errorf("pushWorthy = %v, want %v", got, c.want)
			}
		})
	}
}

// TestOnPushNotificationSet_ReplacesDefaultConfig verifies that repeated Set
// without an explicit config ID replaces the task's default config (rather than
// accumulating duplicates), while a distinct explicit ID registers an additional
// config.
func TestOnPushNotificationSet_ReplacesDefaultConfig(t *testing.T) {
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(noopSender()))
	ctx := context.Background()
	taskID := "replace-task"

	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, URL: "https://a",
	}); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, URL: "https://b",
	}); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	list, err := manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Configs) != 1 {
		t.Fatalf("expected 1 config after re-Set without ID, got %d", len(list.Configs))
	}
	if list.Configs[0].URL != "https://b" {
		t.Errorf("expected the latest URL to win, got %q", list.Configs[0].URL)
	}

	// A distinct explicit ID registers an additional config.
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, ID: "extra", URL: "https://c",
	}); err != nil {
		t.Fatalf("explicit-ID Set: %v", err)
	}
	list, _ = manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	if len(list.Configs) != 2 {
		t.Fatalf("expected 2 configs with distinct IDs, got %d", len(list.Configs))
	}
	got, err := manager.OnPushNotificationGet(ctx, protocol.GetTaskPushNotificationConfigParams{
		TaskID: taskID,
		ID:     "extra",
	})
	if err != nil {
		t.Fatalf("Get explicit config: %v", err)
	}
	if got.ID != "extra" || got.URL != "https://c" {
		t.Fatalf("Get explicit config = %+v, want ID extra and URL https://c", got)
	}
}

// TestManualPushDelivery demonstrates the manual-push pattern: with
// push.Config.ManualDelivery the framework does not auto-deliver. The agent
// stays in control and delivers on its own schedule, reusing the public
// push.HTTPSender plus the configs a client registered via OnPushNotificationSet/List.
func TestManualPushDelivery(t *testing.T) {
	received := make(chan []byte, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case received <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// ManualDelivery keeps push enabled (registration, config RPCs) while
	// the framework delivers nothing — the agent pushes on its own schedule.
	manager := newTestManager(t, echoExecutor(), WithPushConfig(push.Config{
		Sender:         push.NewHTTPSender(),
		ManualDelivery: true,
	}))
	ctx := context.Background()

	taskID := "manual-task"
	seedTask(manager, protocol.Task{
		ID:     taskID,
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, URL: ts.URL,
	}); err != nil {
		t.Fatalf("OnPushNotificationSet: %v", err)
	}

	// Agent-controlled delivery on its own schedule: reuse the public sender and
	// the registered configs. Nothing was pushed automatically.
	sender := push.NewHTTPSender()
	list, err := manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	if err != nil {
		t.Fatalf("OnPushNotificationList: %v", err)
	}
	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
	})
	for _, cfg := range list.Configs {
		if err := sender.SendPush(ctx, cfg, event); err != nil {
			t.Fatalf("manual SendPush: %v", err)
		}
	}

	select {
	case body := <-received:
		var rr protocol.StreamResponse
		if err := json.Unmarshal(body, &rr); err != nil {
			t.Fatalf("webhook body is not a StreamResponse: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual push did not reach the webhook")
	}
}

// TestOnPushNotificationGet_DistinguishesConfigNotFound: an existing task with
// no config must NOT be reported as "task not found" (-32001); a genuinely
// missing task must.
func TestOnPushNotificationGet_DistinguishesConfigNotFound(t *testing.T) {
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(noopSender()))
	ctx := context.Background()

	seedTask(manager, protocol.Task{
		ID:     "task-without-config",
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})

	_, err := manager.OnPushNotificationGet(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: "task-without-config"})
	if !errors.Is(err, taskmanager.ErrPushConfigNotFoundSentinel) {
		t.Errorf("expected PushConfigNotFound for an existing task without config, got %v", err)
	}

	_, err = manager.OnPushNotificationGet(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: "no-such-task"})
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("expected TaskNotFound for a missing task, got %v", err)
	}
}

// TestInlinePushConfig_GatedAndPersisted: an inline message/send push config is
// a registration — rejected with -32003 when push is not enabled, persisted
// (queryable via the config RPCs) when it is.
func TestInlinePushConfig_GatedAndPersisted(t *testing.T) {
	inlineParams := func() protocol.SendMessageParams {
		p := userParams("hello")
		p.Configuration = &protocol.SendMessageConfiguration{
			PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/inline-hook"},
		}
		return p
	}

	t.Run("gated without sender", func(t *testing.T) {
		manager := newTestManager(t, echoExecutor())
		_, err := manager.OnSendMessage(context.Background(), inlineParams())
		if !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
			t.Fatalf("expected PushNotificationNotSupported for inline config without sender, got %v", err)
		}
	})

	t.Run("persisted with sender", func(t *testing.T) {
		var taskID atomic.Pointer[string]
		processor := funcExecutor(
			func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
				id := ec.TaskID
				taskID.Store(&id)
				out := make(chan protocol.StreamEvent, 1)
				out <- agentReply("ok")
				close(out)
				return out, nil
			})
		manager := newTestManager(t, processor, WithPushNotifications(noopSender()))

		if _, err := manager.OnSendMessage(context.Background(), inlineParams()); err != nil {
			t.Fatalf("send failed: %v", err)
		}
		id := taskID.Load()
		if id == nil {
			t.Fatal("processor did not run")
		}
		list, err := manager.OnPushNotificationList(context.Background(),
			protocol.ListTaskPushNotificationConfigsParams{TaskID: *id})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list.Configs) != 1 || list.Configs[0].URL != "https://example.com/inline-hook" {
			t.Fatalf("inline config was not persisted, got %+v", list.Configs)
		}
	})
}

// TestPushSenderAccessor: the server probes this accessor for auto-discovery.
func TestPushSenderAccessor(t *testing.T) {
	withPush := newTestManager(t, echoExecutor(), WithPushNotifications(noopSender()))
	if withPush.PushSender() == nil {
		t.Error("expected a non-nil Sender when push is enabled")
	}
	without := newTestManager(t, echoExecutor())
	if without.PushSender() != nil {
		t.Error("expected a nil Sender when push is not enabled")
	}
}

// TestManualWithoutSenderFails pins the invalid config: manual delivery without
// a Sender is a construction error, not silence.
func TestManualWithoutSenderFails(t *testing.T) {
	if _, err := NewTaskManager(echoExecutor(),
		WithPushConfig(push.Config{ManualDelivery: true})); err == nil {
		t.Fatal("ManualDelivery without a Sender must fail construction")
	}
}
