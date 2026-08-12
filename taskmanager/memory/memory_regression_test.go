// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// waitClosed asserts the stream channel closes (draining any pending frames).
func waitClosed(t *testing.T, ch <-chan protocol.StreamResponse) {
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

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// A suspended round's late channel close must not apply close rules to the
// task: round 1 suspends (yielding the slot), round 2 continues and parks the
// task in WORKING, and only then round 1's channel closes — the task must stay
// round 2's, not be marked FAILED by round 1's finish.
func TestFinish_SuspendedRoundLateCloseKeepsContinuationAlive(t *testing.T) {
	round1Gate := make(chan struct{})
	round2Gate := make(chan struct{})
	var round atomic.Int32
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 4)
			if round.Add(1) == 1 {
				go func() {
					defer close(out)
					out <- workingEvent()
					out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more"))
					<-round1Gate // hold the channel open while round 2 runs
				}()
				return out, nil
			}
			go func() {
				defer close(out)
				out <- workingEvent()
				<-round2Gate
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)
	var round1Once, round2Once sync.Once
	openRound1 := func() { round1Once.Do(func() { close(round1Gate) }) }
	openRound2 := func() { round2Once.Do(func() { close(round2Gate) }) }
	defer openRound2()
	defer openRound1()

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("start"))
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

	// Fire the continuation and wait until it parked the task in WORKING.
	followUp := userParams("more")
	followUp.Message.TaskID = &taskID
	respCh := make(chan *protocol.SendMessageResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := manager.OnSendMessage(context.Background(), followUp)
		respCh <- resp
		errCh <- err
	}()
	eventually(t, func() bool {
		state, ok := storedTaskState(manager, taskID)
		return ok && state == protocol.TaskStateWorking
	}, "continuation never reached WORKING")

	// Round 1's channel closes now; its finish must not touch round 2's task.
	openRound1()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		state, _ := storedTaskState(manager, taskID)
		if state == protocol.TaskStateFailed || state == protocol.TaskStateCanceled {
			t.Fatalf("round 1's close clobbered the continuation's task: state=%s", state)
		}
		time.Sleep(2 * time.Millisecond)
	}

	openRound2()
	resp := <-respCh
	if err := <-errCh; err != nil {
		t.Fatalf("continuation failed: %v", err)
	}
	if task := resp.GetTask(); task == nil || task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("expected the continuation to complete the task, got %+v", resp)
	}
}

// After a suspend event the round has yielded: later events from the same
// round are discarded, the stored state stays suspended, and the response
// stream ends at the suspend frame.
func TestYield_PostSuspendEventsDiscarded(t *testing.T) {
	processor := eventsExecutor(
		workingEvent(),
		statusUpdate(protocol.TaskStateInputRequired, agentReply("need more")),
		workingEvent(), // post-suspend: must be discarded
	)
	manager := newTestManager(t, processor)

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("start"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var taskID string
	sawPostSuspend := false
	suspended := false
	for event := range pipe {
		su := event.GetStatusUpdate()
		if su == nil {
			continue
		}
		taskID = su.TaskID
		if suspended {
			sawPostSuspend = true
		}
		if su.Status.State == protocol.TaskStateInputRequired {
			suspended = true
		}
	}
	if !suspended {
		t.Fatal("never received the input-required frame")
	}
	if sawPostSuspend {
		t.Fatal("an event emitted after the suspend frame was delivered")
	}
	eventually(t, func() bool {
		state, ok := storedTaskState(manager, taskID)
		return ok && state == protocol.TaskStateInputRequired
	}, "stored state must stay input-required after the yielded round's extra events")
}

// The no-live cancel path claims the execution slot with a sentinel so a
// continuation cannot register (and then write) concurrently with its
// CANCELED persist.
func TestClaimCancelSlot_BlocksConcurrentRegistration(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	const taskID = "task-claim-slot"

	live, sentinel, yieldDone := manager.runs.claimCancelSlot("", taskID)
	if live != nil || sentinel == nil {
		t.Fatalf("expected to claim the free slot, got live=%v sentinel=%v", live, sentinel)
	}
	if yieldDone != nil {
		t.Fatal("free slot unexpectedly reported a yield handoff")
	}
	if err := manager.runs.register(context.Background(), "", taskID, &execution{cancel: func() {}}); err == nil {
		t.Fatal("registration must be rejected while a cancel sentinel holds the slot")
	}
	manager.runs.deregister("", taskID, sentinel)

	exec := &execution{cancel: func() {}}
	if err := manager.runs.register(context.Background(), "", taskID, exec); err != nil {
		t.Fatalf("registration after sentinel release failed: %v", err)
	}
	manager.runs.release("", taskID, exec)
}

// Cancellation and suspend handoff use the execution registry as their linearization point.
// Whichever operation acquires it first must keep ownership of the execution:
// cancel prevents yielding, while yield makes cancel wait for the handoff.
func TestCancelYieldLinearization(t *testing.T) {
	t.Run("cancel wins", func(t *testing.T) {
		manager := newTestManager(t, echoExecutor())
		const taskID = "task-cancel-wins-yield"
		var cancelCalls atomic.Int32
		exec := &execution{cancel: func() { cancelCalls.Add(1) }}
		if err := manager.runs.register(context.Background(), "", taskID, exec); err != nil {
			t.Fatalf("register execution: %v", err)
		}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { manager.runs.release("", taskID, exec) }) }
		defer release()

		yieldDone, accepted := manager.runs.requestCancel("", taskID, exec)
		if !accepted || yieldDone != nil {
			t.Fatalf("cancel result: accepted=%v yieldDone=%v, want accepted with no handoff", accepted, yieldDone)
		}
		if got := cancelCalls.Load(); got != 1 {
			t.Fatalf("execution cancel calls = %d, want 1", got)
		}
		if manager.runs.beginYield("", taskID, exec) {
			t.Fatal("suspend handoff started after cancellation had already won")
		}

		release()
	})

	t.Run("yield wins", func(t *testing.T) {
		manager := newTestManager(t, echoExecutor())
		const taskID = "task-yield-wins-cancel"
		var cancelCalls atomic.Int32
		exec := &execution{cancel: func() { cancelCalls.Add(1) }}
		if err := manager.runs.register(context.Background(), "", taskID, exec); err != nil {
			t.Fatalf("register execution: %v", err)
		}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { manager.runs.release("", taskID, exec) }) }
		defer release()
		if !manager.runs.beginYield("", taskID, exec) {
			t.Fatal("suspend handoff did not start")
		}
		handoff := exec.yieldDone

		yieldDone, accepted := manager.runs.requestCancel("", taskID, exec)
		if accepted || yieldDone != handoff {
			t.Fatalf("cancel result: accepted=%v yieldDone=%v, want existing handoff %v",
				accepted, yieldDone, handoff)
		}
		if got := cancelCalls.Load(); got != 0 {
			t.Fatalf("execution cancel calls = %d, want 0 while yield owns the slot", got)
		}

		release()
		select {
		case <-handoff:
		default:
			t.Fatal("releasing the yielded execution did not finish its handoff")
		}
	})
}

// A suspend event may already be ready to emit when CancelTask wins the
// execution lock. The event is still persisted and published, but it must not
// yield the slot: closing the processor channel applies the cancellation close
// rule and leaves the task terminally CANCELED.
func TestCancelBeforeSuspendClosePersistsCanceled(t *testing.T) {
	releaseSuspend := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSuspend) }) }
	defer release()
	var processorSawCancel atomic.Bool
	processor := funcExecutor(func(
		ctx context.Context,
		_ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- workingEvent()
			<-releaseSuspend
			processorSawCancel.Store(ctx.Err() != nil)
			out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more"))
		}()
		return out, nil
	})
	manager := newTestManager(t, processor)

	stream, err := manager.OnSendMessageStream(context.Background(), userParams("start"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initialEvent := recvEvent(t, stream)
	initial := initialEvent.GetTask()
	if initial == nil || initial.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("first stream event = %+v, want initial SUBMITTED Task", initialEvent)
	}
	workingEvent := recvEvent(t, stream)
	working := workingEvent.GetStatusUpdate()
	if working == nil || working.Status.State != protocol.TaskStateWorking {
		t.Fatalf("second stream event = %+v, want WORKING", workingEvent)
	}
	taskID := initial.ID

	cancelSnapshot, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnCancelTask: %v", err)
	}
	if cancelSnapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("cancel snapshot state = %s, want WORKING", cancelSnapshot.Status.State)
	}

	// Cancellation has returned, so requestExecutionCancel won before the
	// processor's already-prepared suspend event is allowed to reach the engine.
	release()
	suspendedEvent := recvEvent(t, stream)
	suspended := suspendedEvent.GetStatusUpdate()
	if suspended == nil || suspended.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("post-cancel processor event = %+v, want INPUT_REQUIRED", suspended)
	}
	canceledEvent := recvEvent(t, stream)
	canceled := canceledEvent.GetStatusUpdate()
	if canceled == nil || canceled.Status.State != protocol.TaskStateCanceled || !canceled.Final {
		t.Fatalf("close-rule event = %+v, want final CANCELED", canceled)
	}
	waitClosed(t, stream)

	if !processorSawCancel.Load() {
		t.Fatal("processor emitted its pending suspend event before observing cancellation")
	}
	if got, ok := storedTaskState(manager, taskID); !ok || got != protocol.TaskStateCanceled {
		t.Fatalf("stored state = %s (exists=%v), want CANCELED", got, ok)
	}
}

// Cancel racing a continuation on a suspended task must converge: the task
// ends CANCELED or COMPLETED (never FAILED), and an accepted continuation
// never fails with -32603 "produced no result".
func TestCancel_RacingContinuationConverges(t *testing.T) {
	for i := 0; i < 30; i++ {
		var round atomic.Int32
		processor := funcExecutor(
			func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
				out := make(chan protocol.StreamEvent, 4)
				if round.Add(1) == 1 {
					out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more"))
					close(out)
					return out, nil
				}
				go func() {
					defer close(out)
					out <- workingEvent()
					select {
					case <-ctx.Done():
						// canceled: close without a terminal state (close rule
						// marks CANCELED)
					case <-time.After(time.Millisecond):
						out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
					}
				}()
				return out, nil
			})
		manager := newTestManager(t, processor)

		resp, err := manager.OnSendMessage(context.Background(), userParams("start"))
		if err != nil {
			t.Fatalf("initial send failed: %v", err)
		}
		taskID := resp.GetTask().ID

		followUp := userParams("more")
		followUp.Message.TaskID = &taskID
		contErr := make(chan error, 1)
		cancelErr := make(chan error, 1)
		go func() {
			_, err := manager.OnSendMessage(context.Background(), followUp)
			contErr <- err
		}()
		go func() {
			_, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
			cancelErr <- err
		}()

		if err := <-contErr; err != nil && strings.Contains(err.Error(), "produced no result") {
			t.Fatalf("iteration %d: continuation died with -32603: %v", i, err)
		}
		<-cancelErr // any outcome is legal; convergence is checked on the store

		eventually(t, func() bool {
			state, ok := storedTaskState(manager, taskID)
			return ok && (state == protocol.TaskStateCanceled || state == protocol.TaskStateCompleted)
		}, "task must converge to CANCELED or COMPLETED")
		manager.Close()
	}
}

// A terminal frame ends the response stream even when the MessageProcessor forgets
// to close its channel.
func TestStream_ClosesAtTerminalWithoutChannelClose(t *testing.T) {
	gate := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 2)
			out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
			go func() {
				defer close(out)
				<-gate // the channel stays open long after the terminal event
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)
	defer close(gate)

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	waitClosed(t, pipe)
}

// Close waits for the detached engines: when it returns, every MessageProcessor has
// observed its ctx cancellation and closed its channel, and new requests are
// rejected.
func TestClose_WaitsForDetachedEngines(t *testing.T) {
	var sawCancel atomic.Bool
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 2)
			out <- workingEvent()
			go func() {
				defer close(out)
				<-ctx.Done()
				sawCancel.Store(true)
			}()
			return out, nil
		})
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	recvEvent(t, pipe) // engine is live

	if err := manager.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !sawCancel.Load() {
		t.Fatal("Close returned before the engine wound down")
	}
	if _, err := manager.OnSendMessage(context.Background(), userParams("late")); err == nil {
		t.Fatal("a closed manager must reject new requests")
	}
}

// A suspended round has released its registry slot, so Close cannot discover
// and cancel it later. Publishing the suspend frame must therefore cancel the
// processor context before deregistration, allowing its drain engine to exit.
func TestClose_CancelsYieldedDrainingEngine(t *testing.T) {
	sawCancel := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 1)
			go func() {
				defer close(out)
				out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more"))
				<-ctx.Done()
				close(sawCancel)
			}()
			return out, nil
		})
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	waitClosed(t, pipe)

	// The suspend path itself must cancel the yielded round. Waiting before
	// Close removes the race where Close could find the not-yet-deregistered
	// execution and make an implementation without yield-time cancellation pass.
	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("suspend did not cancel the yielded processor context")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked waiting for a yielded drain engine")
	}
}

// Close marks the manager closed before sweeping subscribers. A resubscribe
// racing with the later engine wait must be rejected rather than appended
// after the sweep and left behind when Close returns.
func TestClose_RejectsResubscribeDuringEngineWait(t *testing.T) {
	sawCancel := make(chan struct{})
	releaseProcessor := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 1)
			out <- workingEvent()
			go func() {
				defer close(out)
				<-ctx.Done()
				close(sawCancel)
				<-releaseProcessor
			}()
			return out, nil
		})
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProcessor) }) }
	defer release()

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	initialEvent := recvEvent(t, pipe)
	initial := initialEvent.GetTask()
	if initial == nil {
		t.Fatal("stream did not begin with a Task")
	}
	recvEvent(t, pipe) // WORKING

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not reach the engine wait")
	}

	resubscribe, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: initial.ID})
	if err == nil || resubscribe != nil {
		t.Fatalf("resubscribe during Close = (%v, %v), want rejection", resubscribe, err)
	}

	release()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after the processor exited")
	}

	manager.taskMu.RLock()
	defer manager.taskMu.RUnlock()
	if len(manager.subscribers) != 0 {
		t.Fatalf("Close left %d subscriber entries", len(manager.subscribers))
	}
}

// A resubscribe stream is dropped (removed and closed) when its client goes
// away, so a suspended task cannot accumulate dead subscribers forever.
func TestResubscribe_ClientDisconnectDropsSubscriber(t *testing.T) {
	processor := eventsExecutor(statusUpdate(protocol.TaskStateInputRequired, agentReply("need more")))
	manager := newTestManager(t, processor)

	resp, err := manager.OnSendMessage(context.Background(), userParams("start"))
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
	waitClosed(t, ch)
	eventually(t, func() bool {
		manager.taskMu.RLock()
		defer manager.taskMu.RUnlock()
		return len(manager.subscribers[newScopedID("", taskID)]) == 0
	}, "disconnected subscriber was not removed")
}

// The request's inline push-notification config reaches the MessageProcessor via
// ExecContext.
func TestSendMessage_PushConfigReachesProcessor(t *testing.T) {
	var got atomic.Pointer[protocol.TaskPushNotificationConfig]
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			got.Store(ec.PushConfig)
			out := make(chan protocol.StreamEvent, 1)
			out <- agentReply("ok")
			close(out)
			return out, nil
		})
	manager := newTestManager(t, processor, WithPushNotifications(push.Config{Sender: noopSender()}))

	params := userParams("hello")
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
