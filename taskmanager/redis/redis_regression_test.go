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

// pollTaskState polls the store until the task reaches want or the deadline passes.
func pollTaskState(t *testing.T, m *TaskManager, taskID string, want protocol.TaskState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last protocol.TaskState
	for time.Now().Before(deadline) {
		if task, err := m.getTaskInternal(context.Background(), "", taskID); err == nil {
			last = task.Status.State
			if last == want {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %s (last seen %s)", taskID, want, last)
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
		stored, err := manager.getTaskInternal(context.Background(), "", taskID)
		if err != nil {
			t.Fatalf("getTaskInternal failed: %v", err)
		}
		if stored.Status.State != protocol.TaskStateCanceled {
			t.Fatalf("terminal CANCELED was overwritten with %s", stored.Status.State)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Cancellation and suspension linearize under cancelMu. When cancellation
// wins, a later suspend event must remain owned by the canceled round so its
// close rule persists CANCELED instead of yielding and swallowing the cancel.
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
		t.Fatalf("cancel snapshot state = %s, want WORKING", snapshot.Status.State)
	}
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not observe accepted cancellation")
	}
	releaseSuspend()

	var sawSuspend, sawCanceled bool
	deadline := time.After(2 * time.Second)
	for !sawCanceled {
		select {
		case event, ok := <-stream:
			if !ok {
				t.Fatalf("stream closed before CANCELED; saw INPUT_REQUIRED=%v", sawSuspend)
			}
			if update := event.GetStatusUpdate(); update != nil {
				switch update.Status.State {
				case protocol.TaskStateInputRequired:
					sawSuspend = true
				case protocol.TaskStateCanceled:
					sawCanceled = true
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for CANCELED; saw INPUT_REQUIRED=%v", sawSuspend)
		}
	}
	if !sawSuspend {
		t.Fatal("processor's post-cancel INPUT_REQUIRED event was not exercised")
	}
	pollTaskState(t, manager, taskID, protocol.TaskStateCanceled)
}

// When suspension wins the same linearization race, cancellation must receive
// the existing handoff channel instead of canceling the yielded execution.
func TestRequestExecutionCancelReturnsWinningYieldHandoff(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	defer manager.Close()
	const taskID = "task-yield-wins-cancel-race"
	var canceled atomic.Bool
	live := &liveExecution{cancel: func() { canceled.Store(true) }}
	if err := manager.registerExecution(context.Background(), "", taskID, live); err != nil {
		t.Fatalf("registerExecution: %v", err)
	}
	released := false
	defer func() {
		if !released {
			manager.releaseExecution("", taskID, live)
		}
	}()

	if !manager.beginExecutionYield("", taskID, live) {
		t.Fatal("beginExecutionYield did not claim the live execution")
	}
	wantHandoff := live.yieldDone
	handoff, accepted := manager.requestExecutionCancel("", taskID, live)
	if accepted {
		t.Fatal("cancellation was accepted after yield won")
	}
	if handoff == nil || handoff != wantHandoff {
		t.Fatalf("handoff = %v, want existing yield channel %v", handoff, wantHandoff)
	}
	if live.cancelRequested.Load() || canceled.Load() {
		t.Fatal("yield-winning execution was canceled")
	}

	manager.releaseExecution("", taskID, live)
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

	raw, err := mr.Get(taskPrefix + taskID)
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
