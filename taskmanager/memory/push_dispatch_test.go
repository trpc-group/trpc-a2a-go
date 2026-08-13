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

// noopSender enables automatic push in tests while discarding every delivery.
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

	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{
		Sender: push.NewHTTPSender(push.WithUnsafeAllowPrivateNetworks()),
	}))

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

// TestTaskManager_PushRejectedWhenDisabled verifies config registration is
// rejected with -32003 when neither automatic nor manual delivery is enabled.
func TestTaskManager_PushRejectedWhenDisabled(t *testing.T) {
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

// TestOnPushNotificationSet_GeneratesResourceIDs verifies that each Create
// without an explicit ID creates a distinct push-config resource.
func TestOnPushNotificationSet_GeneratesResourceIDs(t *testing.T) {
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{Sender: noopSender()}))
	ctx := context.Background()
	taskID := "replace-task"
	seedTask(manager, protocol.Task{ID: taskID, Status: protocol.TaskStatus{State: protocol.TaskStateWorking}})

	first, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, URL: "https://a",
	})
	if err != nil {
		t.Fatalf("first Set: %v", err)
	}
	second, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, URL: "https://b",
	})
	if err != nil {
		t.Fatalf("second Set: %v", err)
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatalf("generated IDs must be distinct: first=%q second=%q", first.ID, second.ID)
	}

	list, err := manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Configs) != 2 {
		t.Fatalf("expected 2 configs after two creates without ID, got %d", len(list.Configs))
	}

	// A distinct explicit ID registers an additional config.
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, ID: "extra", URL: "https://c",
	}); err != nil {
		t.Fatalf("explicit-ID Set: %v", err)
	}
	list, _ = manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID})
	if len(list.Configs) != 3 {
		t.Fatalf("expected 3 configs with distinct IDs, got %d", len(list.Configs))
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
	manager := newTestManager(t, echoExecutor(),
		WithPushNotifications(push.Config{ManualDelivery: true}))
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
	sender := push.NewHTTPSender(push.WithUnsafeAllowPrivateNetworks())
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
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{Sender: noopSender()}))
	ctx := context.Background()

	seedTask(manager, protocol.Task{
		ID:     "task-without-config",
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})

	_, err := manager.OnPushNotificationGet(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: "task-without-config", ID: "missing"})
	if !errors.Is(err, taskmanager.ErrPushConfigNotFoundSentinel) {
		t.Errorf("expected PushConfigNotFound for an existing task without config, got %v", err)
	}

	_, err = manager.OnPushNotificationGet(ctx,
		protocol.GetTaskPushNotificationConfigParams{TaskID: "no-such-task", ID: "missing"})
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("expected TaskNotFound for a missing task, got %v", err)
	}
}

// TestInlinePushConfigRegistration verifies capability gating and lazy-task
// registration: pure-Message rounds leave no orphan, while a materialized task
// persists the config before its first notification is dispatched.
func TestInlinePushConfigRegistration(t *testing.T) {
	inlineParams := func() protocol.SendMessageParams {
		p := userParams("hello")
		p.Configuration = &protocol.SendMessageConfiguration{
			PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/inline-hook"},
		}
		return p
	}

	t.Run("gated when disabled", func(t *testing.T) {
		manager := newTestManager(t, echoExecutor())
		_, err := manager.OnSendMessage(context.Background(), inlineParams())
		if !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
			t.Fatalf("expected PushNotificationNotSupported for inline config while disabled, got %v", err)
		}
	})

	t.Run("rejected continuation leaves no trace", func(t *testing.T) {
		manager := newTestManager(t, eventsExecutor(
			statusUpdate(protocol.TaskStateInputRequired, agentReply("need more input")),
		))
		first, err := manager.OnSendMessage(context.Background(), userParams("start"))
		if err != nil {
			t.Fatalf("first send failed: %v", err)
		}
		suspended := first.GetTask()
		if suspended == nil || suspended.Status.Message == nil {
			t.Fatalf("expected suspended task with status message, got %+v", first)
		}

		followUp := inlineParams()
		followUp.Message.TaskID = &suspended.ID
		_, err = manager.OnSendMessage(context.Background(), followUp)
		if !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
			t.Fatalf("expected rejected inline config, got %v", err)
		}

		after, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: suspended.ID})
		if err != nil {
			t.Fatalf("get task after rejection: %v", err)
		}
		if after.Status.Message == nil ||
			after.Status.Message.MessageID != suspended.Status.Message.MessageID {
			t.Fatalf("rejected continuation changed current status message: before=%+v after=%+v",
				suspended.Status.Message, after.Status.Message)
		}
		if len(after.History) != len(suspended.History) {
			t.Fatalf("rejected continuation changed history length: before=%d after=%d",
				len(suspended.History), len(after.History))
		}
		manager.conversationMu.RLock()
		_, stored := manager.messages[newScopedID("", "", followUp.Message.MessageID)]
		manager.conversationMu.RUnlock()
		if stored {
			t.Fatalf("rejected continuation stored incoming message %s", followUp.Message.MessageID)
		}
		if live := manager.runs.live("", "", suspended.ID); live != nil {
			t.Fatal("rejected continuation did not release its execution slot")
		}
	})

	t.Run("message-only leaves no orphan", func(t *testing.T) {
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
		manager := newTestManager(t, processor,
			WithPushNotifications(push.Config{ManualDelivery: true}))

		if _, err := manager.OnSendMessage(context.Background(), inlineParams()); err != nil {
			t.Fatalf("send failed: %v", err)
		}
		id := taskID.Load()
		if id == nil {
			t.Fatal("processor did not run")
		}
		list, err := manager.OnPushNotificationList(context.Background(),
			protocol.ListTaskPushNotificationConfigsParams{TaskID: *id})
		if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) || list != nil {
			t.Fatalf("message-only round left a queryable config: list=%+v err=%v", list, err)
		}
	})

	t.Run("task event persists config", func(t *testing.T) {
		var taskID atomic.Pointer[string]
		processor := funcExecutor(
			func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
				id := ec.TaskID
				taskID.Store(&id)
				out := make(chan protocol.StreamEvent, 1)
				out <- statusUpdate(protocol.TaskStateCompleted, nil)
				close(out)
				return out, nil
			})
		manager := newTestManager(t, processor,
			WithPushNotifications(push.Config{ManualDelivery: true}))

		if _, err := manager.OnSendMessage(context.Background(), inlineParams()); err != nil {
			t.Fatalf("send failed: %v", err)
		}
		id := taskID.Load()
		list, err := manager.OnPushNotificationList(context.Background(),
			protocol.ListTaskPushNotificationConfigsParams{TaskID: *id})
		if err != nil || len(list.Configs) != 1 || list.Configs[0].ID != *id {
			t.Fatalf("materialized task config: list=%+v err=%v", list, err)
		}
	})
}

// TestSupportsPushNotifications: the server probes this semantic capability.
func TestSupportsPushNotifications(t *testing.T) {
	withPush := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{Sender: noopSender()}))
	if !withPush.SupportsPushNotifications() {
		t.Error("expected push support when a sender is configured")
	}
	manual := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{ManualDelivery: true}))
	if !manual.SupportsPushNotifications() {
		t.Error("expected push support in manual delivery mode")
	}
	without := newTestManager(t, echoExecutor())
	if without.SupportsPushNotifications() {
		t.Error("expected push to be unsupported without a sender or manual delivery")
	}
}

// TestManualWithoutSenderAllowsRegistration pins the separation between push
// capability and automatic transport: manual mode needs no manager-owned Sender.
func TestManualWithoutSenderAllowsRegistration(t *testing.T) {
	manager := newTestManager(t, echoExecutor(),
		WithPushNotifications(push.Config{ManualDelivery: true}))
	seedTask(manager, protocol.Task{
		ID:     "manual-without-sender",
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})
	ctx := context.Background()
	stored, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: "manual-without-sender",
		URL:    "https://example.com/hook",
	})
	if err != nil {
		t.Fatalf("manual registration without Sender: %v", err)
	}
	if _, err := manager.OnPushNotificationGet(ctx, protocol.GetTaskPushNotificationConfigParams{
		TaskID: stored.TaskID, ID: stored.ID,
	}); err != nil {
		t.Fatalf("manual Get without Sender: %v", err)
	}
	list, err := manager.OnPushNotificationList(ctx,
		protocol.ListTaskPushNotificationConfigsParams{TaskID: stored.TaskID})
	if err != nil || len(list.Configs) != 1 {
		t.Fatalf("manual List without Sender: list=%+v err=%v", list, err)
	}
	if err := manager.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{
		TaskID: stored.TaskID, ID: stored.ID,
	}); err != nil {
		t.Fatalf("manual Delete without Sender: %v", err)
	}
}

// TestQueuedPushUsesRegistrationGeneration verifies that deleting and
// re-creating a config with the same resource ID invalidates only the old
// queued deliveries. New deliveries for the re-created config remain valid.
func TestQueuedPushUsesRegistrationGeneration(t *testing.T) { //nolint:gocyclo // Keep the queue lifecycle explicit.
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan protocol.TaskState, 3)
	var calls atomic.Int32
	sender := push.SenderFunc(func(
		ctx context.Context,
		_ protocol.TaskPushNotificationConfig,
		event protocol.StreamResponse,
	) error {
		if calls.Add(1) == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		update := event.GetStatusUpdate()
		if update != nil {
			delivered <- update.Status.State
		}
		return nil
	})
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{
		Sender:                  sender,
		MaxConcurrentDeliveries: 1,
		DeliveryQueueSize:       2,
	}))
	ctx := context.Background()
	const (
		taskID   = "push-generation-task"
		configID = "stable-config"
	)
	seedTask(manager, protocol.Task{
		ID: taskID, Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, ID: configID, URL: "https://example.com/old",
	}); err != nil {
		t.Fatalf("set old config: %v", err)
	}
	oldRegistrations := manager.pushStore.registrations("", "", taskID)
	if len(oldRegistrations) != 1 {
		t.Fatalf("old registrations = %d, want 1", len(oldRegistrations))
	}

	pushEvent := func(state protocol.TaskState) protocol.StreamResponse {
		return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: taskID,
			Status: protocol.TaskStatus{State: state},
		})
	}
	if err := manager.pushDispatcher.Enqueue(
		oldRegistrations, pushEvent(protocol.TaskStateWorking),
	); err != nil {
		t.Fatalf("enqueue blocking delivery: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first delivery did not start")
	}
	if err := manager.pushDispatcher.Enqueue(
		oldRegistrations, pushEvent(protocol.TaskStateFailed),
	); err != nil {
		t.Fatalf("enqueue old queued delivery: %v", err)
	}

	if err := manager.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{
		TaskID: taskID, ID: configID,
	}); err != nil {
		t.Fatalf("delete old config: %v", err)
	}
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: taskID, ID: configID, URL: "https://example.com/new",
	}); err != nil {
		t.Fatalf("re-create config: %v", err)
	}
	newRegistrations := manager.pushStore.registrations("", "", taskID)
	if len(newRegistrations) != 1 {
		t.Fatalf("new registrations = %d, want 1", len(newRegistrations))
	}
	if oldRegistrations[0].Generation == newRegistrations[0].Generation {
		t.Fatal("re-created config retained the old generation")
	}
	if err := manager.pushDispatcher.Enqueue(
		newRegistrations, pushEvent(protocol.TaskStateCompleted),
	); err != nil {
		t.Fatalf("enqueue new-generation delivery: %v", err)
	}

	close(releaseFirst)
	for i, want := range []protocol.TaskState{
		protocol.TaskStateWorking,
		protocol.TaskStateCompleted,
	} {
		select {
		case got := <-delivered:
			if got != want {
				t.Fatalf("delivery %d state = %s, want %s", i+1, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for delivery %d (%s)", i+1, want)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("sender calls = %d, want 2; stale queued delivery was not skipped", got)
	}
}

// TestSuspendWaitsForPushCapacityBeforeHandoff verifies that a suspended task
// is persisted before a full push queue blocks publication, and that a
// continuation waits for the handoff instead of observing an active-run error.
func TestSuspendWaitsForPushCapacityBeforeHandoff(t *testing.T) { //nolint:gocyclo // Concurrent handoff phases stay explicit.
	firstSendStarted := make(chan struct{})
	releaseSender := make(chan struct{})
	var senderCalls atomic.Int32
	sender := push.SenderFunc(func(
		ctx context.Context,
		_ protocol.TaskPushNotificationConfig,
		_ protocol.StreamResponse,
	) error {
		if senderCalls.Add(1) != 1 {
			return nil
		}
		close(firstSendStarted)
		select {
		case <-releaseSender:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	firstRoundDone := make(chan struct{})
	defer close(firstRoundDone)
	taskIDs := make(chan string, 1)
	var rounds atomic.Int32
	processor := funcExecutor(func(
		ctx context.Context,
		ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		switch rounds.Add(1) {
		case 1:
			taskIDs <- ec.TaskID
			out := make(chan protocol.StreamEvent, 1)
			out <- statusUpdate(protocol.TaskStateInputRequired, nil)
			go func() {
				select {
				case <-firstRoundDone:
				case <-ctx.Done():
				}
				close(out)
			}()
			return out, nil
		case 2:
			out := make(chan protocol.StreamEvent, 1)
			out <- statusUpdate(protocol.TaskStateCompleted, nil)
			close(out)
			return out, nil
		default:
			return nil, errors.New("unexpected processor round")
		}
	})
	manager := newTestManager(t, processor, WithPushNotifications(push.Config{
		Sender:                  sender,
		MaxConcurrentDeliveries: 1,
		DeliveryQueueSize:       1,
	}))
	ctx := context.Background()

	// Occupy the only worker and its only queue slot with a separate, valid
	// registration. The suspend notification below must therefore wait.
	const prefillTaskID = "push-prefill-task"
	seedTask(manager, protocol.Task{
		ID: prefillTaskID, Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})
	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: prefillTaskID, ID: "prefill-config", URL: "https://example.com/prefill",
	}); err != nil {
		t.Fatalf("set prefill config: %v", err)
	}
	prefillRegistrations := manager.pushStore.registrations("", "", prefillTaskID)
	prefillEvent := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: prefillTaskID,
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	})
	if err := manager.pushDispatcher.Enqueue(prefillRegistrations, prefillEvent); err != nil {
		t.Fatalf("enqueue in-flight prefill: %v", err)
	}
	select {
	case <-firstSendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("prefill delivery did not start")
	}
	if err := manager.pushDispatcher.Enqueue(prefillRegistrations, prefillEvent); err != nil {
		t.Fatalf("enqueue queued prefill: %v", err)
	}

	request := userParams("start")
	request.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{
			URL: "https://example.com/suspend",
		},
	}
	stream, err := manager.OnSendMessageStream(ctx, request)
	if err != nil {
		t.Fatalf("start stream: %v", err)
	}
	var taskID string
	select {
	case taskID = <-taskIDs:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not report the task ID")
	}

	// Once INPUT_REQUIRED is visible, the yield barrier must already exist. This
	// ordering is what makes a GetTask-driven continuation safe.
	eventually(t, func() bool {
		state, ok := storedTaskState(manager, taskID)
		return ok && state == protocol.TaskStateInputRequired
	}, "task must persist input-required before publication")
	exec := manager.runs.live("", "", taskID)
	yielding := exec != nil && exec.yieldDone != nil
	if !yielding {
		t.Fatal("input-required became visible before the suspend handoff barrier")
	}
	initialEvent := recvEvent(t, stream)
	initial := initialEvent.GetTask()
	if initial == nil || initial.ID != taskID || initial.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("request stream first event = %+v, want initial SUBMITTED Task", initialEvent)
	}

	type continuationResult struct {
		response *protocol.SendMessageResponse
		err      error
	}
	continuationStarted := make(chan struct{})
	continuationDone := make(chan continuationResult, 1)
	go func() {
		continuationStarted <- struct{}{}
		params := userParams("continue")
		params.Message.TaskID = &taskID
		response, err := manager.OnSendMessage(ctx, params)
		continuationDone <- continuationResult{response: response, err: err}
	}()
	<-continuationStarted
	select {
	case result := <-continuationDone:
		t.Fatalf("continuation returned before push capacity was released: response=%+v err=%v",
			result.response, result.err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case event, ok := <-stream:
		t.Fatalf("request stream published update before push enqueue completed: event=%+v open=%v",
			event, ok)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseSender)
	event := recvEvent(t, stream)
	update := event.GetStatusUpdate()
	if update == nil || update.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("stream event after release = %+v, want input-required status", event)
	}
	select {
	case result := <-continuationDone:
		if result.err != nil {
			t.Fatalf("continuation failed after handoff: %v", result.err)
		}
		task := result.response.GetTask()
		if task == nil || task.Status.State != protocol.TaskStateCompleted {
			t.Fatalf("continuation response = %+v, want completed task", result.response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not finish after push capacity was released")
	}
}
