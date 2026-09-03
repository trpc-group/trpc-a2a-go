// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// waitStreamClosed asserts the stream channel closes (draining pending frames).
func waitStreamClosed(t *testing.T, ch <-chan protocol.StreamResponse) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream channel did not close in time")
		}
	}
}

// TestBlockingResponseDoesNotLetLiveTaskExpire verifies that execution control
// renews Task storage even while the engine is blocked delivering a response.
func TestBlockingResponseDoesNotLetLiveTaskExpire(t *testing.T) {
	manager, redisServer := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		artifactEvent("artifact", "payload"),
	), WithExpireTime(300*time.Millisecond),
		WithTaskSubscriberBufferSize(1),
		WithTaskSubscriberBlockingSend(true))
	defer manager.Close()

	stream, err := manager.OnSendMessageStream(
		context.Background(), sendParams("start", "ctx-blocked-ttl"),
	)
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}

	var taskID string
	var journaled bool
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.cancelMu.RLock()
		for key := range manager.executions {
			taskID = key.id
		}
		manager.cancelMu.RUnlock()
		if taskID != "" {
			// The initial Task and WORKING fill the response queue. Once both
			// events are journaled, the artifact broadcast is blocked.
			if count, _ := manager.client.XLen(
				context.Background(), streamKey("", "", taskID),
			).Result(); count == 2 {
				journaled = true
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("execution never materialized a task")
	}
	if !journaled {
		t.Fatal("execution did not block after journaling the initial events")
	}

	for i := 0; i < 5; i++ {
		redisServer.FastForward(75 * time.Millisecond)
		time.Sleep(60 * time.Millisecond)
	}
	if _, err := manager.getTaskInternal(context.Background(), "", "", taskID); err != nil {
		t.Fatalf("live task expired while response delivery was blocked: %v", err)
	}
	if exists, err := manager.client.Exists(
		context.Background(), executionKey("", "", taskID),
	).Result(); err != nil || exists != 1 {
		t.Fatalf("execution record not live: exists=%d err=%v", exists, err)
	}

	waitStreamClosed(t, stream)
}

// pollTaskState polls the store until the task reaches want or the deadline passes.
func pollTaskState(t *testing.T, m *TaskManager, taskID string, want protocol.TaskState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last protocol.TaskState
	for time.Now().Before(deadline) {
		if task, err := m.getTaskInternal(context.Background(), "", "", taskID); err == nil {
			last = task.Status.State
			if last == want {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %s (last seen %s)", taskID, want, last)
}

// A suspended round releases its execution slot before its processor channel
// necessarily closes. Cancel the yielded round itself so Close can wait for a
// cooperative drain engine even though that execution is no longer discoverable.
func TestSuspend_CancelsYieldedRoundAndCloseWaitsForDrain(t *testing.T) {
	sawCancel := make(chan struct{})
	releaseDrain := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
			<-ctx.Done()
			close(sawCancel)
			<-releaseDrain
		}()
		return out, nil
	})
	manager, _ := setupTest(t, processor)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDrain) }) }
	defer release()

	stream, err := manager.OnSendMessageStream(context.Background(), sendParams("go", "ctx-yield-close"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	waitStreamClosed(t, stream)

	// This must happen before Close. Otherwise Close could win the small window
	// before deregistration and hide the missing yield-time cancellation.
	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("suspend did not cancel the yielded processor context")
	}
	deadline := time.Now().Add(2 * time.Second)
	for liveExecutionCount(manager) != 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := liveExecutionCount(manager); got != 0 {
		t.Fatalf("yielded execution was not released: %d live", got)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before the yielded drain engine finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked after the yielded drain engine finished")
	}
}

// A terminal state persisted by a no-live cancel must survive a yielded
// round's late events: once round 1 suspends (yielding ownership), whatever it
// emits afterwards is discarded instead of overwriting the store.
func TestTerminal_NotResurrectedByYieldedRound(t *testing.T) {
	gate := make(chan struct{})
	processor := executorFunc(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 4)
			go func() {
				defer close(out)
				out <- statusEvent(protocol.TaskStateWorking, nil)
				out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
				<-gate
				out <- statusEvent(protocol.TaskStateWorking, nil) // post-yield: must be discarded
			}()
			return out, nil
		})
	manager, _ := setupTest(t, processor)
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()

	pipe, err := manager.OnSendMessageStream(context.Background(), sendParams("start", ""))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var taskID string
	for taskID == "" {
		event := recvEvent(t, pipe)
		if su := event.GetStatusUpdate(); su != nil && su.Status.State == protocol.TaskStateInputRequired {
			taskID = su.TaskID
		}
	}

	// The round yielded at the suspend frame, so the cancel takes the no-live
	// path and persists CANCELED.
	task, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("expected CANCELED from the no-live cancel, got %s", task.Status.State)
	}

	// Round 1 now emits a late WORKING and closes; the terminal state must hold.
	openGate()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		stored, err := manager.getTaskInternal(context.Background(), "", "", taskID)
		if err != nil {
			t.Fatalf("getTaskInternal failed: %v", err)
		}
		if stored.Status.State != protocol.TaskStateCanceled {
			t.Fatalf("terminal CANCELED was overwritten with %s", stored.Status.State)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestCancelWinsConcurrentSuspendPersistsCanceled verifies cancellation intent
// fences a later suspend event and lets the owner persist CANCELED.
func TestCancelWinsConcurrentSuspendPersistsCanceled(t *testing.T) {
	cancelObserved := make(chan struct{})
	emitSuspend := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 2)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-ctx.Done()
			close(cancelObserved)
			<-emitSuspend
			// Deliberately emit a suspend event after accepting cancellation.
			// The engine must not let it yield ownership away from the close rule.
			out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
		}()
		return out, nil
	})
	manager, _ := setupTest(t, processor)
	defer manager.Close()
	var emitOnce sync.Once
	releaseSuspend := func() { emitOnce.Do(func() { close(emitSuspend) }) }
	defer releaseSuspend()

	stream, err := manager.OnSendMessageStream(context.Background(), sendParams("start", ""))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	if task := initial.GetTask(); task == nil || task.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("first stream frame = %+v, want submitted Task", initial.Result)
	}
	firstEvent := recvEvent(t, stream)
	working := firstEvent.GetStatusUpdate()
	if working == nil || working.Status.State != protocol.TaskStateWorking {
		t.Fatalf("first stream frame = %+v, want WORKING", working)
	}
	taskID := working.TaskID

	// The processor cannot emit INPUT_REQUIRED until OnCancelTask has published
	// cancelRequested and canceled its context, making cancel the deterministic
	// winner of the linearization race.
	snapshot, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnCancelTask: %v", err)
	}
	if snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("cancel snapshot state = %s, want current WORKING", snapshot.Status.State)
	}
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not observe accepted cancellation")
	}
	releaseSuspend()

	var sawCanceled bool
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case event, ok := <-stream:
			if !ok {
				break drain
			}
			if update := event.GetStatusUpdate(); update != nil {
				switch update.Status.State {
				case protocol.TaskStateInputRequired:
					t.Fatal("post-cancel INPUT_REQUIRED bypassed execution fence")
				case protocol.TaskStateCanceled:
					sawCanceled = true
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for CANCELED")
		}
	}
	if !sawCanceled {
		t.Fatal("stream closed before CANCELED")
	}
	pollTaskState(t, manager, taskID, protocol.TaskStateCanceled)
}

// TestCancelLocalExecutionReturnsWinningYieldHandoff verifies that suspension
// wins by exposing its handoff channel instead of canceling the yielded run.
func TestCancelLocalExecutionReturnsWinningYieldHandoff(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	defer manager.Close()
	const taskID = "task-yield-wins-cancel-race"
	var canceled atomic.Bool
	live := &liveExecution{cancel: func() { canceled.Store(true) }}
	if err := manager.registerExecution(context.Background(), "", "", taskID, live); err != nil {
		t.Fatalf("registerExecution: %v", err)
	}
	released := false
	defer func() {
		if !released {
			manager.releaseExecution("", "", taskID, live)
		}
	}()

	if !manager.beginExecutionYield("", "", taskID, live) {
		t.Fatal("beginExecutionYield did not claim the live execution")
	}
	wantHandoff := live.yieldDone
	handoff, accepted := manager.cancelLocalExecution("", "", taskID, live)
	if accepted {
		t.Fatal("cancellation was accepted after yield won")
	}
	if handoff == nil || handoff != wantHandoff {
		t.Fatalf("handoff = %v, want existing yield channel %v", handoff, wantHandoff)
	}
	if live.cancelRequested.Load() || canceled.Load() {
		t.Fatal("yield-winning execution was canceled")
	}

	manager.releaseExecution("", "", taskID, live)
	released = true
	select {
	case <-handoff:
	case <-time.After(time.Second):
		t.Fatal("handoff did not close when the yielding execution deregistered")
	}
}

// A transient storage error during tasks/cancel must not cancel a healthy run:
// canceling is irreversible, a lookup blip is not.
func TestCancel_TransientStorageErrorDoesNotCancelRun(t *testing.T) {
	release := make(chan struct{})
	var ctxCanceled atomic.Bool
	processor := executorFunc(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 4)
			go func() {
				defer close(out)
				out <- statusEvent(protocol.TaskStateWorking, nil)
				<-release
				if ctx.Err() != nil {
					ctxCanceled.Store(true)
					return
				}
				out <- statusEvent(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager, mr := setupTest(t, processor)
	var releaseOnce sync.Once
	openRelease := func() { releaseOnce.Do(func() { close(release) }) }
	defer openRelease()

	pipe, err := manager.OnSendMessageStream(context.Background(), sendParams("start", ""))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	initial := recvEvent(t, pipe)
	if initial.GetTask() == nil {
		t.Fatalf("first stream frame = %+v, want Task", initial.Result)
	}
	event := recvEvent(t, pipe)
	su := event.GetStatusUpdate()
	if su == nil {
		t.Fatalf("expected a status frame, got %+v", event)
	}
	taskID := su.TaskID

	// Storage blips: the cancel must surface the error and leave the run alone.
	mr.SetError("storage blip")
	if _, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID}); err == nil {
		t.Fatal("expected the cancel to fail on a storage error")
	}
	mr.SetError("")

	// The run must complete untouched.
	openRelease()
	pollTaskState(t, manager, taskID, protocol.TaskStateCompleted)
	if ctxCanceled.Load() {
		t.Fatal("a storage blip during cancel canceled a healthy run")
	}
}

// Close waits for the detached engines, so an in-flight run's close-rule
// CANCELED lands in the store before the Redis client goes away.
func TestClose_PersistsCanceledBeforeClientClose(t *testing.T) {
	processor := executorFunc(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 2)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			go func() {
				defer close(out)
				<-ctx.Done()
				// Exercise a late suspend after local shutdown cancellation. The
				// suspended write must keep the lease because cancel already owns
				// the local handoff; the close rule then persists CANCELED.
				out <- statusEvent(protocol.TaskStateInputRequired, nil)
			}()
			return out, nil
		})
	manager, mr := setupTest(t, processor)

	pipe, err := manager.OnSendMessageStream(context.Background(), sendParams("start", ""))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	initial := recvEvent(t, pipe)
	if initial.GetTask() == nil {
		t.Fatalf("first stream frame = %+v, want Task", initial.Result)
	}
	event := recvEvent(t, pipe)
	su := event.GetStatusUpdate()
	if su == nil {
		t.Fatalf("expected a status frame, got %+v", event)
	}
	taskID := su.TaskID

	if err := manager.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	raw, err := mr.Get(taskKey("", "", taskID))
	if err != nil {
		t.Fatalf("task missing from store after Close: %v", err)
	}
	var stored protocol.Task
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("failed to decode stored task: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("expected CANCELED persisted before client close, got %s", stored.Status.State)
	}
}

// A terminal frame ends the response stream even when the MessageProcessor forgets
// to close its channel.
func TestStream_ClosesAtTerminalWithoutChannelClose(t *testing.T) {
	gate := make(chan struct{})
	processor := executorFunc(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 2)
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("done"))
			go func() {
				defer close(out)
				<-gate // the channel stays open long after the terminal event
			}()
			return out, nil
		})
	manager, _ := setupTest(t, processor)
	defer close(gate)

	pipe, err := manager.OnSendMessageStream(context.Background(), sendParams("go", ""))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	waitStreamClosed(t, pipe)
}

// A resubscribe stream closes when its client goes away, so a suspended task
// cannot retain a Stream tailer forever.
func TestResubscribe_ClientDisconnectClosesTailer(t *testing.T) {
	processor := scriptedExecutor(statusEvent(protocol.TaskStateInputRequired, agentReply("need more")))
	manager, _ := setupTest(t, processor)

	resp, err := manager.OnSendMessage(context.Background(), sendParams("start", ""))
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	taskID := resp.GetTask().ID

	subCtx, subCancel := context.WithCancel(context.Background())
	ch, err := manager.OnResubscribe(subCtx, protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnResubscribe failed: %v", err)
	}
	recvEvent(t, ch) // first frame: the snapshot

	subCancel()
	waitStreamClosed(t, ch)
}

// The request's inline push-notification config reaches the MessageProcessor via
// ExecContext.
func TestSendMessage_PushConfigReachesProcessor(t *testing.T) {
	var got atomic.Pointer[protocol.TaskPushNotificationConfig]
	processor := executorFunc(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			got.Store(ec.PushConfig)
			out := make(chan protocol.StreamEvent, 1)
			out <- agentReply("ok")
			close(out)
			return out, nil
		})
	manager, _ := setupTest(t, processor, WithPushNotifications(push.Config{Sender: &recordingSender{}}))

	params := sendParams("hello", "")
	params.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/hook"},
	}
	if _, err := manager.OnSendMessage(context.Background(), params); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	cfg := got.Load()
	if cfg == nil || cfg.URL != "https://example.com/hook" {
		t.Fatalf("push config did not reach the processor: %+v", cfg)
	}
}
