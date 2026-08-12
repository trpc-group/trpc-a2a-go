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
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
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

// firstBlockingSender blocks its first delivery until release is closed, then
// records it and lets every later delivery complete normally.
type firstBlockingSender struct {
	recordingSender
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *firstBlockingSender) SendPush(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	block := false
	s.once.Do(func() {
		block = true
		close(s.started)
	})
	if block {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.recordingSender.SendPush(ctx, cfg, event)
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
	assertTaskManagerCode(t, err, taskmanager.ErrCodePushNotificationNotSupported)
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

func TestRedisStreamingInitialTaskIsNotPushed(t *testing.T) {
	sender := &recordingSender{}
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	), WithPushNotifications(push.Config{Sender: sender}))
	defer m.Close()

	task := storedTask(t, m, "task-stream-push", "ctx-stream-push", protocol.TaskStateWorking)
	if _, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		ID:     "stream-hook",
		URL:    "https://example.com/stream-hook",
	}); err != nil {
		t.Fatalf("OnPushNotificationSet: %v", err)
	}
	params := sendParams("continue", task.ContextID)
	params.Message.TaskID = &task.ID
	stream, err := m.OnSendMessageStream(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	frames := collectStream(t, stream)
	if len(frames) != 2 || frames[0].GetTask() == nil || frames[1].GetStatusUpdate() == nil {
		t.Fatalf("stream frames = %+v, want Task then completed status", frames)
	}

	// Queue a recognizable sentinel for the same task/config. Dispatcher FIFO
	// ordering for one registration guarantees every earlier delivery is
	// recorded before the sentinel, without relying on timing.
	sentinel := agentReply("push-sentinel")
	m.dispatchPush("", task.ID, protocol.NewStreamResponseMessage(sentinel))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		calls := sender.snapshot()
		if len(calls) > 0 {
			last := calls[len(calls)-1].event.GetMessage()
			if last != nil && last.Parts[0].TextContent() == "push-sentinel" {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls := sender.snapshot()
	if len(calls) == 0 || calls[len(calls)-1].event.GetMessage() == nil ||
		calls[len(calls)-1].event.GetMessage().Parts[0].TextContent() != "push-sentinel" {
		t.Fatalf("sentinel push was not delivered: %+v", calls)
	}
	beforeSentinel := calls[:len(calls)-1]
	if len(beforeSentinel) != 1 {
		t.Fatalf("push deliveries before sentinel = %d, want only the real status update", len(beforeSentinel))
	}
	if beforeSentinel[0].event.GetTask() != nil {
		t.Fatalf("synthetic initial Task was pushed: %+v", beforeSentinel[0].event.Result)
	}
	if update := beforeSentinel[0].event.GetStatusUpdate(); update == nil ||
		update.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("push event = %+v, want completed status", beforeSentinel[0].event.Result)
	}
}

func TestRedisPushQueuedGenerationInvalidatedAcrossManagers(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sender := &firstBlockingSender{started: started, release: release}
	managerA, mr := setupTest(t, scriptedExecutor(), WithPushNotifications(push.Config{
		Sender:                  sender,
		MaxConcurrentDeliveries: 1,
		DeliveryQueueSize:       2,
	}))
	defer managerA.Close()
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFirst()

	clientB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	managerB, err := NewTaskManager(scriptedExecutor(), clientB,
		WithPushNotifications(push.Config{ManualDelivery: true}))
	if err != nil {
		t.Fatalf("NewTaskManager B: %v", err)
	}
	defer managerB.Close()

	task := storedTask(t, managerA, "task-cross-generation", "ctx-cross-generation", protocol.TaskStateWorking)
	const configID = "shared-hook"
	if _, err := managerB.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		ID:     configID,
		URL:    "https://old.example/hook",
	}); err != nil {
		t.Fatalf("manager B initial Set: %v", err)
	}
	response := func(state protocol.TaskState) protocol.StreamResponse {
		return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID:    task.ID,
			ContextID: task.ContextID,
			Status:    protocol.TaskStatus{State: state},
		})
	}

	// Keep the first delivery in flight and leave the old-generation event in
	// A's process-local queue.
	managerA.dispatchPush("", task.ID, response(protocol.TaskStateWorking))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first push delivery did not start")
	}
	managerA.dispatchPush("", task.ID, response(protocol.TaskStateInputRequired))

	// B deletes and recreates the same config ID. Existence and ID checks alone
	// would revive the queued event; only the shared Redis generation rejects it.
	if err := managerB.OnPushNotificationDelete(context.Background(),
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: task.ID, ID: configID}); err != nil {
		t.Fatalf("manager B Delete: %v", err)
	}
	if _, err := managerB.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID,
		ID:     configID,
		URL:    "https://new.example/hook",
	}); err != nil {
		t.Fatalf("manager B recreate: %v", err)
	}
	managerA.dispatchPush("", task.ID, response(protocol.TaskStateCompleted))
	releaseFirst()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls := sender.snapshot()
		if len(calls) > 0 {
			last := calls[len(calls)-1]
			if update := last.event.GetStatusUpdate(); update != nil &&
				update.Status.State == protocol.TaskStateCompleted {
				if len(calls) != 2 {
					t.Fatalf("deliveries = %d, want in-flight old plus new; queued old generation was not skipped", len(calls))
				}
				if state := calls[0].event.GetStatusUpdate().Status.State; state != protocol.TaskStateWorking {
					t.Fatalf("first delivery state = %s, want %s", state, protocol.TaskStateWorking)
				}
				if last.cfg.URL != "https://new.example/hook" {
					t.Fatalf("recreated config URL = %q, want new URL", last.cfg.URL)
				}
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("recreated registration was not delivered; calls = %+v", sender.snapshot())
}

func TestRedisPushBackpressureSerializesSuspendContinuation(t *testing.T) { //nolint:gocyclo // Concurrent handoff phases stay explicit.
	started := make(chan struct{})
	release := make(chan struct{})
	sender := &firstBlockingSender{started: started, release: release}
	var round atomic.Int32
	taskIDs := make(chan string, 2)
	processor := executorFunc(func(
		_ context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		taskIDs <- ec.TaskID
		out := make(chan protocol.StreamEvent, 1)
		if round.Add(1) == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
		} else {
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("done"))
		}
		close(out)
		return out, nil
	})
	manager, _ := setupTest(t, processor, WithPushNotifications(push.Config{
		Sender:                  sender,
		MaxConcurrentDeliveries: 1,
		DeliveryQueueSize:       1,
	}))
	defer manager.Close()
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFirst()

	// Occupy the sole worker and fill its one-slot queue so the next enqueue
	// cannot complete until the sender is released.
	prefill := storedTask(t, manager, "task-prefill", "ctx-prefill", protocol.TaskStateWorking)
	if _, err := manager.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: prefill.ID,
		ID:     "prefill-hook",
		URL:    "https://prefill.example/hook",
	}); err != nil {
		t.Fatalf("prefill Set: %v", err)
	}
	prefillResponse := func(state protocol.TaskState) protocol.StreamResponse {
		return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: prefill.ID,
			Status: protocol.TaskStatus{State: state},
		})
	}
	manager.dispatchPush("", prefill.ID, prefillResponse(protocol.TaskStateWorking))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prefill push delivery did not start")
	}
	manager.dispatchPush("", prefill.ID, prefillResponse(protocol.TaskStateSubmitted))

	params := sendParams("start", "ctx-push-suspend")
	params.Configuration = &protocol.SendMessageConfiguration{PushConfig: &protocol.TaskPushNotificationConfig{
		ID:  "suspend-hook",
		URL: "https://suspend.example/hook",
	}}
	stream, err := manager.OnSendMessageStream(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	var taskID string
	select {
	case taskID = <-taskIDs:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not receive first round")
	}
	waitTaskState(t, manager, taskID, protocol.TaskStateInputRequired)

	// Once INPUT_REQUIRED is visible in Redis, the yield barrier must already
	// exist. This ordering makes a GetTask-driven continuation safe.
	manager.cancelMu.RLock()
	live := manager.executions[newScopedID("", taskID)]
	yielding := live != nil && live.yieldDone != nil
	manager.cancelMu.RUnlock()
	if !yielding {
		t.Fatal("input-required became visible before the suspend handoff barrier")
	}
	initial := recvEvent(t, stream)
	if task := initial.GetTask(); task == nil || task.ID != taskID ||
		task.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("initial stream event = %+v, want submitted Task", initial.Result)
	}

	type continuationResult struct {
		response *protocol.SendMessageResponse
		err      error
	}
	continuationDone := make(chan continuationResult, 1)
	go func() {
		followUp := sendParams("more", "")
		followUp.Message.TaskID = &taskID
		response, err := manager.OnSendMessage(context.Background(), followUp)
		continuationDone <- continuationResult{response: response, err: err}
	}()

	select {
	case result := <-continuationDone:
		t.Fatalf("continuation returned before suspend enqueue was released: response=%+v err=%v",
			result.response, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case event, ok := <-stream:
		t.Fatalf("stream exposed suspend frame before push enqueue completed: event=%+v open=%v", event, ok)
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()
	event := recvEvent(t, stream)
	update := event.GetStatusUpdate()
	if update == nil || update.TaskID != taskID || update.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("suspend frame = %+v, want INPUT_REQUIRED for %s", event, taskID)
	}

	select {
	case result := <-continuationDone:
		if result.err != nil {
			t.Fatalf("continuation after suspend handoff: %v", result.err)
		}
		task := result.response.GetTask()
		if task == nil || task.Status.State != protocol.TaskStateCompleted {
			t.Fatalf("continuation response = %+v, want completed task", result.response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not complete after releasing push backpressure")
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
