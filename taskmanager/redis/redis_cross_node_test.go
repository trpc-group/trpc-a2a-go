// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2/internal/executionlease"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// twoNodeManagers builds two RedisTaskManagers sharing one miniredis: nodeA
// runs procA, nodeB is a bystander instance a resubscribe may land on.
func twoNodeManagers(
	t *testing.T,
	procA taskmanager.MessageProcessor,
	opts ...TaskManagerOption,
) (*TaskManager, *TaskManager) {
	t.Helper()
	mr := miniredis.RunT(t)
	ca := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { ca.Close(); cb.Close() })
	a, err := NewTaskManager(procA, ca, opts...)
	if err != nil {
		t.Fatalf("nodeA NewTaskManager: %v", err)
	}
	b, err := NewTaskManager(scriptedExecutor(), cb, opts...)
	if err != nil {
		t.Fatalf("nodeB NewTaskManager: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// blockAfterCommandHook pauses one successful command after Redis has executed
// it, allowing lifecycle tests to hold an RPC between storage and admission.
type blockAfterCommandHook struct {
	name    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockAfterFinalRenewBackend struct {
	executionlease.Backend
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockAfterFinalRenewBackend) CheckAndRenewExecution(
	ctx context.Context,
	tenant, owner, taskID, runID string,
) (bool, bool, error) {
	owned, canceled, err := b.Backend.CheckAndRenewExecution(ctx, tenant, owner, taskID, runID)
	if b.calls.Add(1) == 2 {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return owned, canceled, err
}

// nonBlockingEmptyTransport is a valid polling transport that currently has
// no events. It verifies the manager, rather than every implementation, owns
// the idle-read backoff contract.
type nonBlockingEmptyTransport struct {
	reads atomic.Int64
}

// readSequenceTransport returns a configurable number of read failures before
// one event. It exercises the manager-owned retry and cancellation behavior
// without depending on go-redis command retries.
type readSequenceTransport struct {
	reads         atomic.Int64
	failures      int64
	event         protocol.StreamResponse
	returnedEvent chan struct{}
	returnOnce    sync.Once
}

func (*nonBlockingEmptyTransport) CommitTaskEvent(
	context.Context,
	string,
	string,
	*protocol.Task,
	protocol.StreamResponse,
	bool,
) error {
	return nil
}

func (*nonBlockingEmptyTransport) AppendEvent(
	context.Context,
	string,
	string,
	string,
	protocol.StreamResponse,
) error {
	return nil
}

func (*nonBlockingEmptyTransport) LoadTaskAndCursor(
	context.Context,
	string,
	string,
	string,
) (*protocol.Task, string, error) {
	return &protocol.Task{
		ID: "task-empty-poll",
		Status: protocol.TaskStatus{
			State: protocol.TaskStateWorking,
		},
	}, "cursor", nil
}

func (*nonBlockingEmptyTransport) RefreshTaskLease(
	context.Context,
	string,
	string,
	string,
) error {
	return nil
}

func (t *nonBlockingEmptyTransport) ReadAfter(
	context.Context,
	string,
	string,
	string,
	string,
) ([]protocol.StreamResponse, string, error) {
	t.reads.Add(1)
	return nil, "cursor", nil
}

func (*readSequenceTransport) CommitTaskEvent(
	context.Context,
	string,
	string,
	*protocol.Task,
	protocol.StreamResponse,
	bool,
) error {
	return nil
}

func (*readSequenceTransport) AppendEvent(
	context.Context,
	string,
	string,
	string,
	protocol.StreamResponse,
) error {
	return nil
}

func (*readSequenceTransport) LoadTaskAndCursor(
	context.Context,
	string,
	string,
	string,
) (*protocol.Task, string, error) {
	return &protocol.Task{
		ID: "task-read-sequence",
		Status: protocol.TaskStatus{
			State: protocol.TaskStateWorking,
		},
	}, "cursor", nil
}

func (*readSequenceTransport) RefreshTaskLease(
	context.Context,
	string,
	string,
	string,
) error {
	return nil
}

func (t *readSequenceTransport) ReadAfter(
	context.Context,
	string,
	string,
	string,
	string,
) ([]protocol.StreamResponse, string, error) {
	read := t.reads.Add(1)
	if read <= t.failures {
		return nil, "cursor", errors.New("temporary stream read failure")
	}
	if t.event.Result == nil {
		return nil, "cursor", nil
	}
	t.returnOnce.Do(func() {
		if t.returnedEvent != nil {
			close(t.returnedEvent)
		}
	})
	return []protocol.StreamResponse{t.event}, "next", nil
}

func (h *blockAfterCommandHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *blockAfterCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && cmd.Name() == h.name {
			h.once.Do(func() {
				close(h.entered)
				<-h.release
			})
		}
		return err
	}
}

func (h *blockAfterCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func recvTimeout(t *testing.T, ch <-chan protocol.StreamResponse) (protocol.StreamResponse, bool) {
	t.Helper()
	select {
	case r, ok := <-ch:
		return r, ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a resubscribe stream frame")
		return protocol.StreamResponse{}, false
	}
}

// drainForState reads until it sees a status frame in wantState (returning true)
// or the channel closes (returning whether it was seen). It fails on timeout.
func drainForState(t *testing.T, ch <-chan protocol.StreamResponse, wantState protocol.TaskState) bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	saw := false
	for {
		select {
		case r, ok := <-ch:
			if !ok {
				return saw // channel closed: the tailer self-terminated
			}
			if su := r.GetStatusUpdate(); su != nil && su.Status.State == wantState {
				saw = true
			}
		case <-deadline:
			t.Fatalf("timed out draining the resubscribe stream (saw %s = %v)", wantState, saw)
			return saw
		}
	}
}

// A resubscribe landing on a different instance than the one running the task
// receives the current snapshot and subsequent events within the bounded Redis
// stream retention, and its stream closes on the terminal frame.
func TestCrossNode_ResubscribeReceivesSubsequentEvents(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	procA := executorFunc(func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, agentReply("working"))
			close(started)
			<-release
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("done"))
		}()
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, procA)

	returnImmediately := true
	params := sendParams("go", "ctx-x")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	respA, err := nodeA.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("nodeA OnSendMessage: %v", err)
	}
	taskA := respA.GetTask()
	if taskA == nil {
		t.Fatalf("expected a task snapshot from nodeA, got %+v", respA)
	}
	<-started // the working event is persisted; the task is live on nodeA

	chB, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: taskA.ID})
	if err != nil {
		t.Fatalf("nodeB OnResubscribe: %v", err)
	}

	// The first frame must be the current snapshot (non-terminal).
	first, ok := recvTimeout(t, chB)
	if !ok {
		t.Fatal("nodeB did not receive the initial snapshot")
	}
	if task := first.GetTask(); task == nil || task.Status.State != protocol.TaskStateWorking {
		t.Fatalf("first frame must be the working snapshot, got %+v", first)
	}

	// The terminal event is produced on nodeA AFTER nodeB resubscribed elsewhere.
	close(release)

	if !drainForState(t, chB, protocol.TaskStateCompleted) {
		t.Fatal("cross-node resubscriber never received the completed event before the stream closed")
	}
	if got, err := nodeA.client.XLen(context.Background(), streamKey("", "", taskA.ID)).Result(); err != nil || got != 2 {
		t.Fatalf("each status must be appended exactly once: xlen=%d err=%v", got, err)
	}
}

// An event published right after the atomic snapshot/cursor read, before the
// tailer's first journal read, is still delivered.
func TestCrossNode_DeliversEventPublishedRightAfterResubscribe(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-gap", "ctx-gap", protocol.TaskStateWorking)
	if err := nodeA.appendTaskEvent(context.Background(), "", "", task.ID, protocol.NewStreamResponseStatusUpdate(
		statusEvent(protocol.TaskStateWorking, agentReply("w1")))); err != nil {
		t.Fatalf("append working event: %v", err)
	}

	chB, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	// Publish the terminal event immediately after resubscribe.
	if err := nodeA.appendTaskEvent(context.Background(), "", "", task.ID, protocol.NewStreamResponseStatusUpdate(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")))); err != nil {
		t.Fatalf("append completed event: %v", err)
	}

	first, _ := recvTimeout(t, chB)
	if first.GetTask() == nil {
		t.Fatalf("first frame must be the snapshot, got %+v", first)
	}
	if !drainForState(t, chB, protocol.TaskStateCompleted) {
		t.Fatal("resubscriber lost the event published right after resubscribe (cursor gap)")
	}
}

// A cancel of a task with no live run atomically persists CANCELED with its
// event, so a cross-node resubscriber sees the terminal frame.
func TestCrossNode_CancelWithoutLiveRunReachesResubscriber(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-cancel", "ctx-cancel", protocol.TaskStateWorking)

	chB, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if first, _ := recvTimeout(t, chB); first.GetTask() == nil {
		t.Fatalf("expected the snapshot first, got %+v", first)
	}

	if _, err := nodeA.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID}); err != nil {
		t.Fatalf("nodeA OnCancelTask: %v", err)
	}

	if !drainForState(t, chB, protocol.TaskStateCanceled) {
		t.Fatal("cross-node resubscriber never received the CANCELED event")
	}
}

// TestCrossNode_CancelActiveExecutionStopsOwnerAndFencesLateEvent verifies distributed cancellation.
func TestCrossNode_CancelActiveExecutionStopsOwnerAndFencesLateEvent(t *testing.T) {
	processorCanceled := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 2)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-ctx.Done()
			close(processorCanceled)
			// A processor may race one last event after observing cancellation.
			// The distributed intent fence must reject it before the owner
			// close rule commits CANCELED.
			out <- statusEvent(protocol.TaskStateInputRequired, agentReply("late"))
		}()
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor, WithExpireTime(600*time.Millisecond))

	stream, err := nodeA.OnSendMessageStream(context.Background(), sendParams("start", "ctx-active-cancel"))
	if err != nil {
		t.Fatalf("nodeA OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	if initial.GetTask() == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}
	workingFrame := recvEvent(t, stream)
	working := workingFrame.GetStatusUpdate()
	if working == nil || working.Status.State != protocol.TaskStateWorking {
		t.Fatalf("working frame = %+v", working)
	}

	snapshot, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: working.TaskID})
	if err != nil {
		t.Fatalf("nodeB OnCancelTask: %v", err)
	}
	if snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("cancel snapshot state = %s, want current WORKING", snapshot.Status.State)
	}
	if nodeB.liveRun("", "", working.TaskID) != nil {
		t.Fatal("cancel request unexpectedly depended on a nodeB-local execution")
	}

	select {
	case <-processorCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("nodeA processor did not observe cross-node cancellation")
	}

	sawCanceled := false
	for event := range stream {
		if update := event.GetStatusUpdate(); update != nil &&
			update.Status.State == protocol.TaskStateInputRequired {
			t.Fatal("late INPUT_REQUIRED bypassed the execution fence")
		} else if update != nil && update.Status.State == protocol.TaskStateCanceled {
			sawCanceled = true
		}
	}
	if !sawCanceled {
		t.Fatal("owner stream did not receive the committed CANCELED event")
	}
	stored, err := nodeB.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: working.TaskID})
	if err != nil {
		t.Fatalf("nodeB OnGetTask: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored state = %s, want CANCELED", stored.Status.State)
	}
}

func TestCrossNode_CancelAcknowledgedAfterContextDelivery(t *testing.T) {
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateWorking, nil)
		go func() {
			<-ctx.Done()
			close(out)
		}()
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor, WithExpireTime(600*time.Millisecond))
	stream, err := nodeA.OnSendMessageStream(context.Background(), sendParams("start", "ctx-cancel-delivery"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	task := initial.GetTask()
	if task == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}
	if update := recvEvent(t, stream); update.GetStatusUpdate() == nil ||
		update.GetStatusUpdate().Status.State != protocol.TaskStateWorking {
		t.Fatalf("working frame = %+v", update.Result)
	}

	cancelEntered := make(chan struct{})
	releaseCancel := make(chan struct{})
	var cancelOnce sync.Once
	nodeA.cancelMu.Lock()
	live := nodeA.executions[newScopedID("", "", task.ID)]
	if live == nil {
		nodeA.cancelMu.Unlock()
		t.Fatal("live execution not registered")
	}
	originalCancel := live.cancel
	live.cancel = func() {
		cancelOnce.Do(func() { close(cancelEntered) })
		<-releaseCancel
		originalCancel()
	}
	nodeA.cancelMu.Unlock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCancel) }) }
	t.Cleanup(release)

	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	select {
	case <-cancelEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not begin local context cancellation")
	}
	if got := nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_acknowledged",
	).Val(); got != "0" {
		t.Fatalf("cancel_acknowledged = %q before context delivery, want 0", got)
	}
	select {
	case got := <-cancelDone:
		t.Fatalf("remote cancel returned before context delivery: task=%+v err=%v", got.task, got.err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case got := <-cancelDone:
		if got.err != nil || got.task == nil || got.task.Status.State != protocol.TaskStateWorking {
			t.Fatalf("remote cancel = (%+v, %v), want accepted WORKING snapshot", got.task, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not return after context delivery")
	}
	if !drainForState(t, stream, protocol.TaskStateCanceled) {
		t.Fatal("owner stream never reached CANCELED")
	}
}

// TestCrossNode_TerminalCancelAcknowledgedAfterContextDelivery verifies that a
// processor terminal state may win a remote cancellation only after the owner
// has delivered cancellation to the processor context.
func TestCrossNode_TerminalCancelAcknowledgedAfterContextDelivery(t *testing.T) {
	emitTerminal := make(chan struct{})
	contextCanceled := make(chan struct{})
	processorDone := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(processorDone)
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-emitTerminal
			out <- statusEvent(protocol.TaskStateCompleted, nil)
			<-ctx.Done()
			close(contextCanceled)
		}()
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor)
	stream, err := nodeA.OnSendMessageStream(context.Background(), sendParams("start", "ctx-terminal-cancel-delivery"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	task := initial.GetTask()
	if task == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}
	workingFrame := recvEvent(t, stream)
	if update := workingFrame.GetStatusUpdate(); update == nil ||
		update.Status.State != protocol.TaskStateWorking {
		t.Fatalf("working frame = %+v", update)
	}

	// Ensure the terminal commit, rather than the control sweep, is what observes
	// the pending cancellation.
	nodeA.controlCancel()
	nodeA.controlWg.Wait()
	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(emitTerminal)

	select {
	case got := <-cancelDone:
		if got.err != nil || got.task == nil || got.task.Status.State != protocol.TaskStateWorking {
			t.Fatalf("remote cancel = (%+v, %v), want accepted WORKING snapshot", got.task, got.err)
		}
		select {
		case <-contextCanceled:
		default:
			t.Fatal("remote cancel returned before terminal winner received context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not return after terminal commit")
	}
	select {
	case <-processorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not exit after terminal cancellation delivery")
	}
	if !drainForState(t, stream, protocol.TaskStateCompleted) {
		t.Fatal("owner stream never reached terminal COMPLETED state")
	}
	stored, err := nodeB.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil || stored.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("stored task = %+v, err=%v; want COMPLETED", stored, err)
	}
}

// TestCrossNode_CancelDuringAdmissionPreventsProcessorStart verifies that a
// remote cancellation requested during continuation preparation prevents the
// owner from entering user processor code.
func TestCrossNode_CancelDuringAdmissionPreventsProcessorStart(t *testing.T) {
	var processorCalls atomic.Int32
	continuationInvoked := make(chan error, 1)
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		if processorCalls.Add(1) == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, nil)
		} else {
			continuationInvoked <- ctx.Err()
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}
		close(out)
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor)

	seed, err := nodeA.OnSendMessage(context.Background(), sendParams("seed", "ctx-admission-cancel"))
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	task := seed.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("seed response = %+v, want INPUT_REQUIRED Task", seed)
	}

	// Pause after continuation history has been read. AcquireExecution and the
	// first CheckAndRenewExecution have completed, but ProcessMessage has not.
	hook := &blockAfterCommandHook{
		name:    "lrange",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseHook := func() { releaseOnce.Do(func() { close(hook.release) }) }
	t.Cleanup(releaseHook)
	nodeA.client.AddHook(hook)

	result := make(chan error, 1)
	go func() {
		params := sendParams("continue", task.ContextID)
		params.Message.TaskID = &task.ID
		_, err := nodeA.OnSendMessage(context.Background(), params)
		result <- err
	}()
	select {
	case <-hook.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not reach the admission history read")
	}

	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case got := <-cancelDone:
		t.Fatalf("remote cancel returned before owner crossed admission: task=%+v err=%v", got.task, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseHook()
	select {
	case got := <-cancelDone:
		if got.err != nil {
			t.Fatalf("remote OnCancelTask: %v", got.err)
		}
		if got.task.Status.State != protocol.TaskStateInputRequired {
			t.Fatalf("remote cancel state = %s, want current INPUT_REQUIRED", got.task.Status.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not return after owner acknowledged admission")
	}

	select {
	case err := <-result:
		if !errors.Is(err, taskmanager.ErrInvalidParamsSentinel) {
			t.Fatalf("continuation error = %v, want invalid params", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not return after remote cancellation")
	}
	select {
	case ctxErr := <-continuationInvoked:
		t.Fatalf("processor started after cancellation was requested; ctx.Err=%v", ctxErr)
	default:
	}
	if got := processorCalls.Load(); got != 1 {
		t.Fatalf("processor calls = %d, want only the seed round", got)
	}
	if live := nodeA.liveRun("", "", task.ID); live != nil {
		t.Fatal("canceled admission left a local execution registered")
	}
	if exists, err := nodeA.client.Exists(
		context.Background(), executionKey("", "", task.ID),
	).Result(); err != nil || exists != 0 {
		t.Fatalf("canceled admission execution lease: exists=%d err=%v, want absent", exists, err)
	}
	stored, err := nodeA.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil || stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored task after canceled admission = %+v, err=%v; want CANCELED", stored, err)
	}
}

// TestCrossNode_CancelObservedAfterFinalRenewDoesNotStartProcessor verifies the
// local admission gate when an owner observes cancellation after its final
// distributed lease check but before entering user code.
func TestCrossNode_CancelObservedAfterFinalRenewDoesNotStartProcessor(t *testing.T) {
	var processorCalls atomic.Int32
	processor := executorFunc(func(
		_ context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		if processorCalls.Add(1) == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, nil)
		} else {
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}
		close(out)
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor)

	seed, err := nodeA.OnSendMessage(context.Background(), sendParams("seed", "ctx-final-renew-cancel"))
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	task := seed.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("seed response = %+v, want INPUT_REQUIRED Task", seed)
	}
	nodeA.controlCancel()
	nodeA.controlWg.Wait()

	backend := &blockAfterFinalRenewBackend{
		Backend: nodeA.executionLease,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	nodeA.executionLease = backend
	var releaseOnce sync.Once
	releaseRenew := func() { releaseOnce.Do(func() { close(backend.release) }) }
	t.Cleanup(releaseRenew)
	continuationDone := make(chan error, 1)
	go func() {
		params := sendParams("continue", task.ContextID)
		params.Message.TaskID = &task.ID
		_, err := nodeA.OnSendMessage(context.Background(), params)
		continuationDone <- err
	}()
	select {
	case <-backend.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not finish its final lease check")
	}

	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	live := nodeA.liveRun("", "", task.ID)
	if live == nil {
		t.Fatal("continuation was not registered locally")
	}
	// Deterministically model the control sweep observing the Redis intent in
	// the narrow window while the final lease check is returning.
	if _, accepted := nodeA.cancelLocalExecution("", "", task.ID, live); !accepted {
		t.Fatal("owner did not observe remote cancellation")
	}
	select {
	case got := <-cancelDone:
		t.Fatalf("cancel returned before processor admission resolved: task=%+v err=%v", got.task, got.err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseRenew()
	select {
	case got := <-cancelDone:
		if got.err != nil {
			t.Fatalf("remote OnCancelTask: %v", got.err)
		}
		if got.task == nil || got.task.Status.State != protocol.TaskStateInputRequired {
			t.Fatalf("cancel snapshot = %+v, want INPUT_REQUIRED", got.task)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not return after admission resolved")
	}
	select {
	case err := <-continuationDone:
		if !errors.Is(err, taskmanager.ErrInvalidParamsSentinel) {
			t.Fatalf("continuation error = %v, want invalid params", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not finish")
	}
	if got := processorCalls.Load(); got != 1 {
		t.Fatalf("processor calls = %d, want only seed round", got)
	}
	stored, err := nodeB.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil || stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored task = %+v, err=%v; want CANCELED", stored, err)
	}
}

// TestCrossNode_ReleasePreservesUnobservedCancel verifies that an owner whose
// processor closes before the next control sweep cannot delete remote intent.
func TestCrossNode_ReleasePreservesUnobservedCancel(t *testing.T) {
	var calls atomic.Int32
	continuationStarted := make(chan struct{})
	closeContinuation := make(chan struct{})
	processor := executorFunc(func(
		_ context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		if calls.Add(1) == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, nil)
			close(out)
			return out, nil
		}
		close(continuationStarted)
		go func() {
			<-closeContinuation
			close(out)
		}()
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor)

	seed, err := nodeA.OnSendMessage(context.Background(), sendParams("seed", "ctx-release-cancel"))
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	task := seed.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("seed response = %+v, want INPUT_REQUIRED Task", seed)
	}
	// Stop automatic observation so release itself must preserve and finish the
	// cancellation intent.
	nodeA.controlCancel()
	nodeA.controlWg.Wait()

	continuationDone := make(chan error, 1)
	go func() {
		params := sendParams("continue", task.ContextID)
		params.Message.TaskID = &task.ID
		_, err := nodeA.OnSendMessage(context.Background(), params)
		continuationDone <- err
	}()
	select {
	case <-continuationStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not start")
	}

	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(closeContinuation)

	select {
	case err := <-continuationDone:
		if err != nil {
			t.Fatalf("continuation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not finish")
	}
	select {
	case got := <-cancelDone:
		if got.err != nil {
			t.Fatalf("remote OnCancelTask: %v", got.err)
		}
		if got.task == nil || got.task.Status.State != protocol.TaskStateInputRequired {
			t.Fatalf("cancel response = %+v, want accepted INPUT_REQUIRED snapshot", got.task)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not finish after owner release")
	}
	stored, err := nodeB.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil || stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored task = %+v, err=%v; want CANCELED", stored, err)
	}
	if exists, err := nodeB.client.Exists(
		context.Background(), executionKey("", "", task.ID),
	).Result(); err != nil || exists != 0 {
		t.Fatalf("execution record = %d, err=%v; want absent", exists, err)
	}
}

// TestCrossNode_CloseRuleHonorsUnobservedCancel verifies that a framework
// close rule cannot turn an accepted remote cancellation into FAILED merely
// because the owner has not yet observed the intent in its control sweep.
func TestCrossNode_CloseRuleHonorsUnobservedCancel(t *testing.T) {
	closeProcessor := make(chan struct{})
	processor := executorFunc(func(
		_ context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateWorking, nil)
		go func() {
			<-closeProcessor
			close(out)
		}()
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor)
	stream, err := nodeA.OnSendMessageStream(context.Background(), sendParams("start", "ctx-close-rule-cancel"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	task := initial.GetTask()
	if task == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}
	workingFrame := recvEvent(t, stream)
	if update := workingFrame.GetStatusUpdate(); update == nil ||
		update.Status.State != protocol.TaskStateWorking {
		t.Fatalf("working frame = %+v", update)
	}

	nodeA.controlCancel()
	nodeA.controlWg.Wait()
	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(closeProcessor)

	select {
	case got := <-cancelDone:
		if got.err != nil || got.task == nil || got.task.Status.State != protocol.TaskStateWorking {
			t.Fatalf("remote cancel = (%+v, %v), want accepted WORKING snapshot", got.task, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not finish after processor close")
	}
	if !drainForState(t, stream, protocol.TaskStateCanceled) {
		t.Fatal("owner close rule did not publish CANCELED")
	}
	stored, err := nodeB.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil || stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored task = %+v, err=%v; want CANCELED", stored, err)
	}
}

func TestCrossNode_CancelCommitsAfterOwnerLeaseExpires(t *testing.T) {
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateWorking, nil)
		go func() {
			<-ctx.Done()
			close(out)
		}()
		return out, nil
	})
	nodeA, redisServer := setupTest(t, processor, WithExpireTime(600*time.Millisecond))
	t.Cleanup(func() { _ = nodeA.Close() })
	baseTime := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	redisServer.SetTime(baseTime)
	nodeBClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = nodeBClient.Close() })
	nodeB, err := NewTaskManager(scriptedExecutor(), nodeBClient, WithExpireTime(600*time.Millisecond))
	if err != nil {
		t.Fatalf("nodeB NewTaskManager: %v", err)
	}
	t.Cleanup(func() { _ = nodeB.Close() })

	stream, err := nodeA.OnSendMessageStream(context.Background(), sendParams("start", "ctx-owner-expiry"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	task := initial.GetTask()
	if task == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}
	workingFrame := recvEvent(t, stream)
	working := workingFrame.GetStatusUpdate()
	if working == nil || working.Status.State != protocol.TaskStateWorking {
		t.Fatalf("working frame = %+v", working)
	}
	nodeA.controlCancel()
	nodeA.controlWg.Wait()

	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	redisServer.SetTime(baseTime.Add(nodeA.executionLeaseDuration + time.Millisecond))

	select {
	case got := <-cancelDone:
		if got.err != nil {
			t.Fatalf("remote OnCancelTask: %v", got.err)
		}
		if got.task == nil || got.task.Status.State != protocol.TaskStateWorking {
			t.Fatalf("cancel response = %+v, want accepted WORKING snapshot", got.task)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not take over after owner lease expiry")
	}
	stored, err := nodeB.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil || stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored task = %+v, err=%v; want CANCELED", stored, err)
	}
}

func TestCrossNode_CancelTimeoutLeavesDurableIntent(t *testing.T) {
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateWorking, nil)
		go func() {
			<-ctx.Done()
			close(out)
		}()
		return out, nil
	})
	nodeA, redisServer := setupTest(t, processor, WithExpireTime(2*time.Second))
	t.Cleanup(func() { _ = nodeA.Close() })
	nodeBClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = nodeBClient.Close() })
	nodeB, err := NewTaskManager(scriptedExecutor(), nodeBClient, WithExpireTime(2*time.Second))
	if err != nil {
		t.Fatalf("nodeB NewTaskManager: %v", err)
	}
	t.Cleanup(func() { _ = nodeB.Close() })

	stream, err := nodeA.OnSendMessageStream(context.Background(), sendParams("start", "ctx-cancel-timeout"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	task := initial.GetTask()
	if task == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}
	workingFrame := recvEvent(t, stream)
	if update := workingFrame.GetStatusUpdate(); update == nil || update.Status.State != protocol.TaskStateWorking {
		t.Fatalf("working frame = %+v", update)
	}
	nodeA.controlCancel()
	nodeA.controlWg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := nodeB.OnCancelTask(ctx, protocol.TaskIDParams{ID: task.ID}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("remote cancel error = %v, want deadline exceeded", err)
	}
	if got := nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_requested",
	).Val(); got != "1" {
		t.Fatalf("cancel_requested = %q, want durable intent", got)
	}
	if got := nodeA.client.HGet(
		context.Background(), executionKey("", "", task.ID), "cancel_acknowledged",
	).Val(); got != "0" {
		t.Fatalf("cancel_acknowledged = %q, want unacknowledged timeout", got)
	}
}

func TestCrossNode_CancelBeforeLazyTaskNeverReturnsNullSuccess(t *testing.T) {
	taskID := make(chan string, 1)
	releaseProcessor := make(chan struct{})
	processor := executorFunc(func(
		_ context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		taskID <- ec.TaskID
		<-releaseProcessor
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateCompleted, nil)
		close(out)
		return out, nil
	})
	nodeA, nodeB := twoNodeManagers(t, processor, WithExpireTime(600*time.Millisecond))
	sendDone := make(chan error, 1)
	go func() {
		_, err := nodeA.OnSendMessage(context.Background(), sendParams("start", "ctx-lazy-cancel"))
		sendDone <- err
	}()
	id := <-taskID

	type cancelResult struct {
		task *protocol.Task
		err  error
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		canceled, err := nodeB.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: id})
		cancelDone <- cancelResult{task: canceled, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.client.HGet(
		context.Background(), executionKey("", "", id), "cancel_requested",
	).Val() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("remote cancellation intent was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(releaseProcessor)

	select {
	case got := <-cancelDone:
		if got.task != nil || !errors.Is(got.err, taskmanager.ErrTaskNotFoundSentinel) {
			t.Fatalf("cancel before lazy task = (%+v, %v), want TaskNotFound", got.task, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote cancel did not finish after processor admission")
	}
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("late terminal send: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late terminal send did not finish")
	}
}

// Task events are journaled by default so a later subscription may resume on
// any replica sharing Redis.
func TestTaskEventsUseStreamByDefault(t *testing.T) {
	mgr, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, agentReply("w")),
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	))
	response, err := mgr.OnSendMessage(context.Background(), sendParams("go", "ctx"))
	if err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("expected Task response, got %+v", response)
	}
	if got, err := mgr.client.XLen(context.Background(), streamKey("", "", task.ID)).Result(); err != nil || got != 2 {
		t.Fatalf("default event stream length = %d, want 2: %v", got, err)
	}
}

func TestSendStreamingInitialTaskDoesNotEnterJournal(t *testing.T) {
	mgr, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, agentReply("working")),
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	))
	stream, err := mgr.OnSendMessageStream(context.Background(), sendParams("go", "ctx-stream-journal"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	frames := collectStream(t, stream)
	if len(frames) != 3 || frames[0].GetTask() == nil || frames[1].GetStatusUpdate() == nil ||
		frames[2].GetStatusUpdate() == nil {
		t.Fatalf("stream frames = %+v, want Task then two status updates", frames)
	}
	taskID := frames[0].GetTask().ID
	if got, err := mgr.client.XLen(context.Background(), streamKey("", "", taskID)).Result(); err != nil || got != 2 {
		t.Fatalf("journal length = %d, want only two processor updates: %v", got, err)
	}
	entries, err := mgr.client.XRange(context.Background(), streamKey("", "", taskID), "-", "+").Result()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	for _, entry := range entries {
		payload, ok := entry.Values[streamField].(string)
		if !ok {
			t.Fatalf("journal payload = %#v, want string", entry.Values[streamField])
		}
		var event protocol.StreamResponse
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("decode journal event: %v", err)
		}
		if event.GetTask() != nil {
			t.Fatalf("synthetic initial Task was journaled: %+v", event.Result)
		}
	}
}

func TestFirstTaskEventIndexesTask(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	))
	params := sendParams("go", "ctx-first-event-index")
	params.Tenant = "tenant-a"
	response, err := manager.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("expected Task response, got %+v", response)
	}
	listed, err := manager.OnListTasks(context.Background(), protocol.ListTasksParams{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("OnListTasks: %v", err)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != task.ID {
		t.Fatalf("first event did not index Task %s: %+v", task.ID, listed.Tasks)
	}
}

// Replaying an operation after a newer cross-node write neither duplicates the
// Stream entry nor rolls the Task snapshot back. This models go-redis retrying
// an EVALSHA whose successful reply was lost on the network.
func TestTaskEventCommitIsIdempotentAcrossInterleavedWrite(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	transport := manager.eventTransport.(*redisTaskEventTransport)
	working := &protocol.Task{
		ID:        "task-idempotent-commit",
		ContextID: "ctx-idempotent-commit",
		Status:    protocol.TaskStatus{State: protocol.TaskStateWorking},
	}
	workingEvent := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: working.ID, ContextID: working.ContextID, Status: working.Status,
	})
	if err := transport.commitTaskEventWithOperationID(
		context.Background(), "tenant-a", "",
		working, workingEvent, true, "op-a"); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	completed := copyTask(working)
	completed.Status.State = protocol.TaskStateCompleted
	completedEvent := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: completed.ID, ContextID: completed.ContextID, Status: completed.Status,
	})
	if err := transport.commitTaskEventWithOperationID(
		context.Background(), "tenant-a", "",
		completed, completedEvent, false, "op-b"); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if err := transport.commitTaskEventWithOperationID(
		context.Background(), "tenant-a", "",
		working, workingEvent, false, "op-a"); err != nil {
		t.Fatalf("replayed first commit: %v", err)
	}

	if got, err := manager.client.XLen(
		context.Background(), streamKey("tenant-a", "", working.ID),
	).Result(); err != nil || got != 2 {
		t.Fatalf("stream length after replay = %d, want 2: %v", got, err)
	}
	stored, err := manager.getTaskInternal(context.Background(), "tenant-a", "", working.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("replayed operation rolled task back to %s", stored.Status.State)
	}
	if got, err := manager.client.ZCard(
		context.Background(), streamDedupeKey("tenant-a", "", working.ID),
	).Result(); err != nil || got != 3 {
		t.Fatalf("dedupe journal size = %d, want two operations plus sequence: %v", got, err)
	}
}

func TestAppendTaskEventIsIdempotent(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	task := storedTask(t, manager, "task-idempotent-append", "ctx-idempotent-append", protocol.TaskStateWorking)
	transport := manager.eventTransport.(*redisTaskEventTransport)
	first := protocol.NewStreamResponseArtifactUpdate(artifactEvent("artifact", "chunk-a"))
	second := protocol.NewStreamResponseArtifactUpdate(artifactEvent("artifact", "chunk-b"))

	if err := transport.appendEventWithOperationID(context.Background(), "", "", task.ID, first, "op-a"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := transport.appendEventWithOperationID(context.Background(), "", "", task.ID, second, "op-b"); err != nil {
		t.Fatalf("second append: %v", err)
	}
	if err := transport.appendEventWithOperationID(context.Background(), "", "", task.ID, first, "op-a"); err != nil {
		t.Fatalf("replayed append: %v", err)
	}
	if got, err := manager.client.XLen(context.Background(), streamKey("", "", task.ID)).Result(); err != nil || got != 2 {
		t.Fatalf("stream length after append replay = %d, want 2: %v", got, err)
	}
}

func TestTaskEventDedupeIsTenantScopedAndTypeChecked(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	transport := manager.eventTransport.(*redisTaskEventTransport)
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		task := &protocol.Task{
			ID: "shared-task", ContextID: "ctx-" + tenant,
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
		}
		event := protocol.NewStreamResponseTask(task)
		if err := transport.commitTaskEventWithOperationID(
			context.Background(), tenant, "",
			task, event, true, "op-shared"); err != nil {
			t.Fatalf("commit for %s: %v", tenant, err)
		}
	}
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if got, err := manager.client.XLen(
			context.Background(), streamKey(tenant, "", "shared-task"),
		).Result(); err != nil || got != 1 {
			t.Fatalf("%s stream length = %d, want 1: %v", tenant, got, err)
		}
	}

	task := storedTask(t, manager, "task-wrong-dedupe", "ctx-wrong-dedupe", protocol.TaskStateWorking)
	before, err := manager.client.Get(context.Background(), taskKey("", "", task.ID)).Bytes()
	if err != nil {
		t.Fatalf("load original task: %v", err)
	}
	if err := manager.client.Set(
		context.Background(), streamDedupeKey("", "", task.ID), "wrong-type", 0,
	).Err(); err != nil {
		t.Fatalf("seed wrong-type dedupe journal: %v", err)
	}
	changed := copyTask(task)
	changed.Status.State = protocol.TaskStateCompleted
	err = transport.commitTaskEventWithOperationID(
		context.Background(), "", "",
		changed, protocol.NewStreamResponseTask(changed), false, "op-wrong-type")

	if err == nil {
		t.Fatal("commit unexpectedly succeeded with wrong-type dedupe journal")
	}
	after, getErr := manager.client.Get(context.Background(), taskKey("", "", task.ID)).Bytes()
	if getErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("failed commit changed task: before=%s after=%s err=%v", before, after, getErr)
	}
	if got, xlenErr := manager.client.XLen(context.Background(), streamKey("", "", task.ID)).Result(); xlenErr != nil || got != 0 {
		t.Fatalf("failed commit partially appended event: len=%d err=%v", got, xlenErr)
	}
}

func TestReadAfterAdvancesPastMalformedEntry(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	task := storedTask(t, manager, "task-malformed-entry", "ctx-malformed-entry", protocol.TaskStateWorking)
	if err := manager.client.XAdd(context.Background(), &redis.XAddArgs{
		Stream: streamKey("", "", task.ID), Values: map[string]interface{}{"unexpected": "value"},
	}).Err(); err != nil {
		t.Fatalf("seed malformed entry: %v", err)
	}
	transport := manager.eventTransport.(*redisTaskEventTransport)
	events, cursor, err := transport.ReadAfter(context.Background(), "", "", task.ID, "0-0")
	if err != nil {
		t.Fatalf("ReadAfter malformed entry: %v", err)
	}
	if len(events) != 0 || cursor == "0-0" {
		t.Fatalf("malformed entry result: events=%+v cursor=%q", events, cursor)
	}
	events, next, err := transport.ReadAfter(context.Background(), "", "", task.ID, cursor)
	if err != nil || len(events) != 0 || next != cursor {
		t.Fatalf("malformed entry was replayed: events=%+v cursor=%q next=%q err=%v", events, cursor, next, err)
	}
}

// When the resubscribing client disconnects (its request context is canceled),
// the stream closes so the server-side drain ends and nothing leaks.
func TestCrossNode_ClientDisconnectClosesStream(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-disc", "ctx-disc", protocol.TaskStateWorking)

	rctx, rcancel := context.WithCancel(context.Background())
	chB, err := nodeB.OnResubscribe(rctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if first, _ := recvTimeout(t, chB); first.GetTask() == nil {
		t.Fatalf("expected the snapshot first, got %+v", first)
	}

	rcancel() // the client goes away

	// The stream must close (draining any buffered frames first).
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-chB:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("resubscribe stream did not close after client disconnect")
		}
	}
}

// A subscription closes after the Task and its event journal expire instead of
// polling a missing stream forever.
func TestStreamResubscribe_TaskExpirationClosesStream(t *testing.T) {
	manager, mr := setupTest(t, scriptedExecutor(), WithExpireTime(time.Second))
	task := storedTask(t, manager, "task-expire", "ctx-expire", protocol.TaskStateWorking)

	stream, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}

	mr.FastForward(2 * time.Second)
	if frame, ok := recvTimeout(t, stream); ok {
		t.Fatalf("stream remained open after Task expiration: %+v", frame)
	}
}

// A live execution renews its Task, event journal, list index, and push config
// while the processor is silent. The lease stops after the terminal event.
func TestStreamResubscribe_LiveTaskRenewsLeaseUntilTerminal(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProcessor := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseProcessor()
	processorDone := make(chan struct{})
	processor := executorFunc(func(
		context.Context, *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(processorDone)
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-release
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	const expiration = time.Second
	manager, mr := setupTest(t, processor, WithExpireTime(expiration))

	returnImmediately := true
	params := sendParams("long task", "ctx-expired-live")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	response, err := manager.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateWorking {
		t.Fatalf("expected working Task, got %+v", response)
	}
	if _, err := manager.storePushConfig(context.Background(), "", protocol.TaskPushNotificationConfig{
		TaskID: task.ID, URL: "https://example.com/live-task",
	}); err != nil {
		t.Fatalf("store push config: %v", err)
	}

	stream, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}

	// Advance more than two full TTLs in smaller steps. A real ticker beat
	// between steps renews from Redis's current clock.
	for i := 0; i < 4; i++ {
		mr.FastForward(700 * time.Millisecond)
		time.Sleep(400 * time.Millisecond)
		for _, key := range []string{
			taskKey("", "", task.ID),
			streamKey("", "", task.ID),
			streamDedupeKey("", "", task.ID),
			pushNotificationKey("", "", task.ID),
		} {
			if !mr.Exists(key) {
				t.Fatalf("live lease did not retain %s after step %d", key, i+1)
			}
		}
	}

	// Force the tenant index member stale; the next heartbeat must restore its
	// score, otherwise ListTasks would prune a Task whose storage is still live.
	if err := manager.client.ZAdd(context.Background(), taskIndexKey("", ""), redis.Z{
		Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: task.ID,
	}).Err(); err != nil {
		t.Fatalf("stale task index: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	listed, err := manager.OnListTasks(context.Background(), protocol.ListTasksParams{})
	if err != nil {
		t.Fatalf("OnListTasks: %v", err)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != task.ID {
		t.Fatalf("heartbeat did not restore list index: %+v", listed.Tasks)
	}

	releaseProcessor()
	select {
	case <-processorDone:
	case <-time.After(5 * time.Second):
		t.Fatal("processor did not finish after releasing the terminal event")
	}

	deadline := time.Now().Add(5 * time.Second)
	for liveExecutionCount(manager) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := liveExecutionCount(manager); got != 0 {
		t.Fatalf("live execution remained registered after terminal event: %d", got)
	}
	if !drainForState(t, stream, protocol.TaskStateCompleted) {
		t.Fatal("resubscriber did not receive terminal event after the silent interval")
	}

	mr.FastForward(2 * expiration)
	if got, err := manager.client.Exists(
		context.Background(),
		taskKey("", "", task.ID),
		streamKey("", "", task.ID),
		streamDedupeKey("", "", task.ID),
		pushNotificationKey("", "", task.ID),
	).Result(); err != nil || got != 0 {
		t.Fatalf("terminal task lease kept renewing: exists=%d err=%v", got, err)
	}
}

func TestContinuationRenewsLeaseBeforeProcessor(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var mr *miniredis.Miniredis
	processor := executorFunc(func(
		context.Context, *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		close(entered)
		<-release
		out := make(chan protocol.StreamEvent)
		close(out)
		return out, nil
	})
	const expiration = time.Second
	manager, redisServer := setupTest(t, processor, WithExpireTime(expiration))
	mr = redisServer
	task := storedTask(t, manager, "task-near-expiry", "ctx-near-expiry", protocol.TaskStateInputRequired)
	mr.FastForward(900 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		params := sendParams("continue", task.ContextID)
		params.Message.TaskID = &task.ID
		_, err := manager.OnSendMessage(context.Background(), params)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("processor was not called")
	}
	if ttl := mr.TTL(taskKey("", "", task.ID)); ttl != expiration {
		t.Fatalf("task TTL at processor entry = %v, want %v", ttl, expiration)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("continuation did not finish")
	}
}

func TestRefreshTaskLeaseDoesNotCreateMissingTask(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor(), WithExpireTime(time.Second))
	err := manager.refreshTaskLease(context.Background(), "tenant-a", "", "missing-task")
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("refresh missing task error = %v, want TaskNotFound", err)
	}
	if got, existsErr := manager.client.Exists(
		context.Background(),
		taskKey("tenant-a", "", "missing-task"),
		streamKey("tenant-a", "", "missing-task"),
		streamDedupeKey("tenant-a", "", "missing-task"),
		taskIndexKey("tenant-a", ""),
		pushNotificationKey("tenant-a", "", "missing-task"),
	).Result(); existsErr != nil || got != 0 {
		t.Fatalf("refresh created missing task storage: exists=%d err=%v", got, existsErr)
	}
}

func TestCrossNodeResubscribeOptionIsDeprecatedNoOp(t *testing.T) {
	opts := DefaultRedisTaskManagerOptions()
	WithCrossNodeResubscribe(false)(opts)
	if !opts.CrossNodeResubscribe {
		t.Fatal("deprecated option disabled the always-on Redis Stream path")
	}
}

// A transient transport error is retried and does not tear down a healthy
// subscription before its next event arrives.
func TestStreamResubscribe_TransientReadErrorRetries(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	transport := &readSequenceTransport{
		failures: 1,
		event: protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: "task-read-sequence",
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		}),
	}
	manager.eventTransport = transport

	stream, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: "task-read-sequence"})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetStatusUpdate() == nil ||
		frame.GetStatusUpdate().Status.State != protocol.TaskStateCompleted {
		t.Fatalf("terminal event missing after retry: %+v", frame)
	}
	if _, ok := recvTimeout(t, stream); ok {
		t.Fatal("stream remained open after terminal event")
	}
	if got := transport.reads.Load(); got != 2 {
		t.Fatalf("ReadAfter calls = %d, want 2", got)
	}
}

// A longer Redis outage does not turn into a clean EOF. The tailer remains
// attached and resumes from the same cursor when storage recovers.
func TestStreamResubscribe_ReadOutageRecovers(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	transport := &readSequenceTransport{
		failures: 4,
		event: protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: "task-read-sequence",
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		}),
	}
	manager.eventTransport = transport

	stream, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: "task-read-sequence"})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetStatusUpdate() == nil ||
		frame.GetStatusUpdate().Status.State != protocol.TaskStateCompleted {
		t.Fatalf("terminal event missing after storage recovery: %+v", frame)
	}
	if _, ok := recvTimeout(t, stream); ok {
		t.Fatal("stream remained open after terminal event")
	}
	if got := transport.reads.Load(); got != 5 {
		t.Fatalf("ReadAfter calls = %d, want 5", got)
	}
}

// Canceling the request releases a tailer even when its size-one response pipe
// is full and the consumer has not drained the initial snapshot.
func TestStreamResubscribe_CancelReleasesBlockedSend(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	returnedEvent := make(chan struct{})
	transport := &readSequenceTransport{
		event: protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: "task-read-sequence",
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
		}),
		returnedEvent: returnedEvent,
	}
	manager.eventTransport = transport

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: "task-read-sequence"})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	select {
	case <-returnedEvent:
	case <-time.After(5 * time.Second):
		t.Fatal("tailer did not read the event")
	}
	cancel()

	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected buffered snapshot, got %+v", frame)
	}
	for {
		if _, ok := recvTimeout(t, stream); !ok {
			break
		}
	}
}

// Close returns promptly and joins an active polling resubscribe tailer.
func TestCrossNode_CloseJoinsActiveTailer(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-close", "ctx-close", protocol.TaskStateWorking)
	if _, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID}); err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	// nodeB's tailer is polling/waiting; Close must cancel and join it.
	done := make(chan error, 1)
	go func() { done <- nodeB.Close() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with an active resubscribe tailer")
	}
}

// Close cancels and joins Stream tailers before waiting for a slow processor
// to finish, while keeping Redis open for the engine's final CANCELED commit.
func TestCrossNode_CloseStopsTailerBeforeSlowEngine(t *testing.T) {
	canceled := make(chan struct{})
	release := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-ctx.Done()
			close(canceled)
			<-release
			close(out)
		}()
		return out, nil
	})
	manager, _ := setupTest(t, processor)
	returnImmediately := true
	params := sendParams("slow close", "ctx-slow-close")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	response, err := manager.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}
	task := response.GetTask()
	stream, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the processor")
	}
	if frame, ok := recvTimeout(t, stream); ok {
		t.Fatalf("tailer remained open while Close waited for engine: %+v", frame)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before slow engine finished: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after slow engine finished")
	}
}

// A task event committed before the atomic snapshot/cursor read is represented
// by the snapshot only. A later event is represented by the stream only.
func TestCrossNode_AtomicSnapshotCursorHasNoOverlapOrGap(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-atomic", "ctx-atomic", protocol.TaskStateWorking)

	first := artifactEvent("artifact", "part-one")
	first.TaskID = task.ID
	first.ContextID = task.ContextID
	task.Artifacts, _ = protocol.AppendArtifact(task.Artifacts, first.Artifact, false)
	if err := nodeA.commitTaskEvent(
		context.Background(), "", "",
		task, protocol.NewStreamResponseArtifactUpdate(first), false); err != nil {
		t.Fatalf("store first task event: %v", err)
	}

	snapshot, cursor, err := nodeB.eventTransport.LoadTaskAndCursor(context.Background(), "", "", task.ID)
	if err != nil {
		t.Fatalf("loadTaskAndCursor: %v", err)
	}
	if len(snapshot.Artifacts) != 1 {
		t.Fatalf("snapshot must contain the committed artifact exactly once, got %+v", snapshot)
	}
	if duplicate, err := nodeB.client.XRead(context.Background(), &redis.XReadArgs{
		Streams: []string{streamKey("", "", task.ID), cursor},
		Count:   1,
		Block:   -1,
	}).Result(); !errors.Is(err, redis.Nil) {
		t.Fatalf("event already represented by snapshot was replayed: events=%+v err=%v", duplicate, err)
	}

	second := artifactEvent("artifact", "part-two")
	second.TaskID = task.ID
	second.ContextID = task.ContextID
	appendChunk := true
	second.Append = &appendChunk
	task.Artifacts, _ = protocol.AppendArtifact(task.Artifacts, second.Artifact, true)
	if err := nodeA.commitTaskEvent(
		context.Background(), "", "",
		task, protocol.NewStreamResponseArtifactUpdate(second), false); err != nil {
		t.Fatalf("store second task event: %v", err)
	}
	streams, err := nodeB.client.XRead(context.Background(), &redis.XReadArgs{
		Streams: []string{streamKey("", "", task.ID), cursor},
		Count:   2,
		Block:   -1,
	}).Result()
	if err != nil || len(streams) != 1 || len(streams[0].Messages) != 1 {
		t.Fatalf("event committed after snapshot was not delivered once: streams=%+v err=%v", streams, err)
	}
}

// input-required ends one execution round, not the task subscription. The
// cross-node stream must remain open for a later continuation's terminal event.
func TestCrossNode_InputRequiredDoesNotCloseResubscribe(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-input", "ctx-input", protocol.TaskStateWorking)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := nodeB.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}

	input := statusEvent(protocol.TaskStateInputRequired, agentReply("need input"))
	input.TaskID = task.ID
	input.ContextID = task.ContextID
	task.Status = input.Status
	if err := nodeA.commitTaskEvent(
		context.Background(), "", "",
		task, protocol.NewStreamResponseStatusUpdate(input), false); err != nil {
		t.Fatalf("store input-required: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetStatusUpdate() == nil ||
		frame.GetStatusUpdate().Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("expected input-required event, got %+v", frame)
	}
	completed := statusEvent(protocol.TaskStateCompleted, agentReply("done"))
	completed.TaskID = task.ID
	completed.ContextID = task.ContextID
	task.Status = completed.Status
	if err := nodeA.commitTaskEvent(
		context.Background(), "", "",
		task, protocol.NewStreamResponseStatusUpdate(completed), false); err != nil {
		t.Fatalf("store completed: %v", err)
	}
	if !drainForState(t, stream, protocol.TaskStateCompleted) {
		t.Fatal("resubscribe did not receive the terminal event after input-required")
	}
}

// A continuation clears the previous Status.Message from the Task snapshot.
// That mutation must be journaled so a subscriber that already received the
// old snapshot observes the clear even when the continuation emits only a
// Message and never produces another task status.
func TestCrossNode_ContinuationStatusMessageClearIsJournaled(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor(agentReply("clarification")))
	task := storedTask(t, nodeA, "task-clear-status", "ctx-clear-status", protocol.TaskStateInputRequired)
	task.Status.Message = agentReply("need input")
	if err := nodeA.storeTask(context.Background(), "", "", task); err != nil {
		t.Fatalf("store task status message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := nodeB.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	initial, ok := recvTimeout(t, stream)
	if !ok || initial.GetTask() == nil || initial.GetTask().Status.Message == nil {
		t.Fatalf("expected initial snapshot with status message, got %+v", initial)
	}

	params := sendParams("more input", task.ContextID)
	params.Message.TaskID = &task.ID
	if _, err := nodeA.OnSendMessage(context.Background(), params); err != nil {
		t.Fatalf("continuation: %v", err)
	}
	cleared, ok := recvTimeout(t, stream)
	if !ok || cleared.GetStatusUpdate() == nil {
		t.Fatalf("expected journaled status clear, got %+v", cleared)
	}
	status := cleared.GetStatusUpdate().Status
	if status.State != protocol.TaskStateInputRequired || status.Message != nil {
		t.Fatalf("unexpected status clear event: %+v", status)
	}
	stored, err := nodeA.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask: %v", err)
	}
	if stored.Status.Message != nil {
		t.Fatalf("continuation did not clear stored status message: %+v", stored.Status)
	}
}

// The Redis tailer is an independent reader, so it uses cancelable blocking
// backpressure instead of treating a temporarily full output buffer as EOF.
func TestCrossNode_SlowConsumerDoesNotLoseStream(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-slow", "ctx-slow", protocol.TaskStateWorking)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := nodeB.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if got := cap(stream); got != 1 {
		t.Fatalf("resubscribe response buffer = %d, want 1", got)
	}
	const updates = 4
	for i := 0; i < updates; i++ {
		update := protocol.NewStreamResponseStatusUpdate(
			statusEvent(protocol.TaskStateWorking, agentReply("still working")),
		)
		if err := nodeA.appendTaskEvent(context.Background(), "", "", task.ID, update); err != nil {
			t.Fatalf("append stream event %d: %v", i, err)
		}
	}

	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}
	for i := 0; i < updates; i++ {
		if frame, ok := recvTimeout(t, stream); !ok || frame.GetStatusUpdate() == nil {
			t.Fatalf("slow consumer lost queued update %d, got %+v", i, frame)
		}
		// Keep the size-one buffer full between reads. The tailer must apply
		// backpressure without losing events from the Redis journal.
		time.Sleep(10 * time.Millisecond)
	}
}

// An idle polling transport must back off exponentially instead of producing a
// fixed-rate Redis read stream when it returns no events or cursor progress.
func TestCrossNode_EmptyTransportReadBacksOff(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	transport := &nonBlockingEmptyTransport{}
	manager.eventTransport = transport

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: "task-empty-poll"})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}
	time.Sleep(650 * time.Millisecond)
	cancel()
	if _, ok := recvTimeout(t, stream); ok {
		t.Fatal("resubscribe stream remained open after cancellation")
	}
	if reads := transport.reads.Load(); reads > 4 {
		t.Fatalf("empty ReadAfter did not back off: reads=%d", reads)
	}
}

// Idle cross-node subscriptions must not consume the Redis connections used by
// Task storage. With a size-one pool, one tailer and one GetTask must coexist.
func TestCrossNode_IdleTailerDoesNotStarveTaskStoragePool(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:        mr.Addr(),
		PoolSize:    1,
		PoolTimeout: 50 * time.Millisecond,
	})
	manager, err := NewTaskManager(scriptedExecutor(), client)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	task := storedTask(t, manager, "task-pool", "ctx-pool", protocol.TaskStateWorking)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	if frame, ok := recvTimeout(t, stream); !ok || frame.GetTask() == nil {
		t.Fatalf("expected initial snapshot, got %+v", frame)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID}); err != nil {
		t.Fatalf("active tailer starved task storage: %v", err)
	}
}

// Storage/cursor errors are surfaced. A malformed stream key must neither be
// treated as an empty stream nor allow a Task update to commit by itself.
func TestCrossNode_WrongTypeStreamFailsAtomically(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-wrongtype", "ctx-wrongtype", protocol.TaskStateWorking)
	if err := nodeA.client.Set(context.Background(), streamKey("", "", task.ID), "not-a-stream", 0).Err(); err != nil {
		t.Fatalf("seed wrong-type stream key: %v", err)
	}

	stream, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err == nil || stream != nil {
		t.Fatalf("wrong-type cursor read must fail: stream=%v err=%v", stream, err)
	}
	if _, err := nodeA.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: task.ID}); err == nil {
		t.Fatal("cancel unexpectedly committed Task without its stream event")
	}
	stored, err := nodeA.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask after failed cancel: %v", err)
	}
	if stored.Status.State != protocol.TaskStateWorking {
		t.Fatalf("failed atomic commit changed Task state to %s", stored.Status.State)
	}
}

// A Message emitted for an existing Task is not reported to the RPC or stored
// in history unless its stream append succeeds.
func TestCrossNode_MessageAppendFailureIsNotExposedAsSuccess(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor(agentReply("reply")))
	task := storedTask(t, manager, "task-message-fail", "ctx-message-fail", protocol.TaskStateWorking)
	if err := manager.client.Set(context.Background(), streamKey("", "", task.ID), "not-a-stream", 0).Err(); err != nil {
		t.Fatalf("seed wrong-type stream key: %v", err)
	}
	params := sendParams("continue", task.ContextID)
	params.Message.TaskID = &task.ID
	response, err := manager.OnSendMessage(context.Background(), params)
	if err == nil {
		t.Fatalf("message append failure was exposed as success: %+v", response)
	}
	if !strings.Contains(err.Error(), "task event stream has wrong type") {
		t.Fatalf("message append returned the wrong error: %v", err)
	}
	history, err := manager.getConversationHistory(context.Background(), "", "", task.ContextID, 100)
	if err != nil {
		t.Fatalf("getConversationHistory: %v", err)
	}
	for _, message := range history {
		if message.Role == protocol.MessageRoleAgent {
			t.Fatalf("message append failure persisted an agent reply: %+v", message)
		}
	}
}

// A failed Task/event commit is an execution error. Neither a status nor an
// artifact working copy may be returned as if it committed successfully.
func TestCrossNode_TaskCommitFailureIsNotExposedAsSuccess(t *testing.T) {
	tests := []struct {
		name              string
		event             protocol.StreamEvent
		returnImmediately bool
	}{
		{name: "status", event: statusEvent(protocol.TaskStateCompleted, agentReply("done"))},
		{
			name:              "status-return-immediately",
			event:             statusEvent(protocol.TaskStateCompleted, agentReply("done")),
			returnImmediately: true,
		},
		{name: "artifact", event: artifactEvent("artifact", "uncommitted")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, _ := setupTest(
				t,
				scriptedExecutor(test.event),
			)
			taskID := "task-" + test.name + "-fail"
			contextID := "ctx-" + test.name + "-fail"
			task := storedTask(t, manager, taskID, contextID, protocol.TaskStateWorking)
			if err := manager.client.Set(context.Background(), streamKey("", "", task.ID), "not-a-stream", 0).Err(); err != nil {
				t.Fatalf("seed wrong-type stream key: %v", err)
			}

			params := sendParams("continue", task.ContextID)
			params.Message.TaskID = &task.ID
			if test.returnImmediately {
				returnImmediately := true
				params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
			}
			response, err := manager.OnSendMessage(context.Background(), params)
			if err == nil {
				t.Fatalf("failed %s commit was exposed as success: %+v", test.name, response)
			}
			if !strings.Contains(err.Error(), "task event stream has wrong type") {
				t.Fatalf("failed %s commit returned the wrong error: %v", test.name, err)
			}
			stored, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
			if err != nil {
				t.Fatalf("OnGetTask after failed %s commit: %v", test.name, err)
			}
			if stored.Status.State != protocol.TaskStateWorking || len(stored.Artifacts) != 0 {
				t.Fatalf("failed %s commit changed stored Task: %+v", test.name, stored)
			}
		})
	}
}

// A known persistence failure is a result immediately. The unary caller must
// not wait for a long-running processor to close its channel while the engine
// drains that channel in the background.
func TestCrossNode_PersistenceFailureReturnsBeforeProcessorCloses(t *testing.T) {
	release := make(chan struct{})
	processorDone := make(chan struct{})
	var releaseOnce sync.Once
	releaseProcessor := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseProcessor()
	processor := executorFunc(func(context.Context, *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(processorDone)
			defer close(out)
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("uncommitted"))
			<-release
		}()
		return out, nil
	})
	manager, _ := setupTest(t, processor)
	task := storedTask(t, manager, "task-fast-failure", "ctx-fast-failure", protocol.TaskStateWorking)
	if err := manager.client.Set(context.Background(), streamKey("", "", task.ID), "not-a-stream", 0).Err(); err != nil {
		t.Fatalf("seed wrong-type stream key: %v", err)
	}

	params := sendParams("continue", task.ContextID)
	params.Message.TaskID = &task.ID
	result := make(chan error, 1)
	go func() {
		_, err := manager.OnSendMessage(context.Background(), params)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "task event stream has wrong type") {
			t.Fatalf("unexpected persistence result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("persistence failure waited for the processor channel to close")
	}
	select {
	case <-processorDone:
		t.Fatal("processor closed before the failure was returned")
	default:
	}
	releaseProcessor()
}

// The streaming response has no asynchronous error return, so a persistence
// failure must at least close it immediately while the processor is drained.
func TestCrossNode_PersistenceFailureClosesStreamBeforeProcessorCloses(t *testing.T) {
	tests := []struct {
		name  string
		event protocol.StreamEvent
	}{
		{name: "status", event: statusEvent(protocol.TaskStateCompleted, agentReply("uncommitted"))},
		{name: "artifact", event: artifactEvent("uncommitted", "content")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := make(chan struct{})
			processorDone := make(chan struct{})
			var releaseOnce sync.Once
			releaseProcessor := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseProcessor()
			processor := executorFunc(func(context.Context, *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
				out := make(chan protocol.StreamEvent)
				go func() {
					defer close(processorDone)
					defer close(out)
					out <- test.event
					<-release
				}()
				return out, nil
			})
			manager, _ := setupTest(t, processor)
			task := storedTask(t, manager, "task-stream-failure-"+test.name,
				"ctx-stream-failure-"+test.name, protocol.TaskStateWorking)
			if err := manager.client.Set(context.Background(), streamKey("", "", task.ID), "not-a-stream", 0).Err(); err != nil {
				t.Fatalf("seed wrong-type stream key: %v", err)
			}

			params := sendParams("continue", task.ContextID)
			params.Message.TaskID = &task.ID
			stream, err := manager.OnSendMessageStream(context.Background(), params)
			if err != nil {
				t.Fatalf("OnSendMessageStream: %v", err)
			}
			select {
			case frame, ok := <-stream:
				if ok {
					t.Fatalf("uncommitted event reached stream: %+v", frame)
				}
			case <-time.After(time.Second):
				t.Fatal("persistence failure left the response stream open")
			}
			select {
			case <-processorDone:
				t.Fatal("processor closed before the response stream")
			default:
			}
			releaseProcessor()
		})
	}
}

// A superseded status message moves to history only after the new status and
// its event commit. If that commit fails, the message remains solely on the
// stored Task rather than appearing in both Task.Status and history.
func TestCrossNode_FailedStatusCommitDoesNotAdvanceHistory(t *testing.T) {
	taskID := make(chan string, 1)
	releaseSecond := make(chan struct{})
	var releaseOnce sync.Once
	releaseProcessor := func() { releaseOnce.Do(func() { close(releaseSecond) }) }
	defer releaseProcessor()
	processor := executorFunc(func(_ context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		taskID <- ec.TaskID
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, agentReply("first"))
			<-releaseSecond
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("second"))
		}()
		return out, nil
	})
	manager, _ := setupTest(t, processor)
	result := make(chan error, 1)
	go func() {
		_, err := manager.OnSendMessage(context.Background(), sendParams("start", "ctx-history-failure"))
		result <- err
	}()
	id := <-taskID

	deadline := time.Now().Add(time.Second)
	for {
		stored, err := manager.getTaskInternal(context.Background(), "", "", id)
		if err == nil && stored.Status.Message != nil &&
			stored.Status.Message.Parts[0].TextContent() == "first" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first status was not persisted: task=%+v err=%v", stored, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := manager.client.Set(context.Background(), streamKey("", "", id), "not-a-stream", 0).Err(); err != nil {
		t.Fatalf("replace stream with wrong type: %v", err)
	}
	releaseProcessor()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "task event stream has wrong type") {
		t.Fatalf("unexpected failed status result: %v", err)
	}

	stored, err := manager.getTaskInternal(context.Background(), "", "", id)
	if err != nil {
		t.Fatalf("get stored task: %v", err)
	}
	if stored.Status.State != protocol.TaskStateWorking || stored.Status.Message == nil ||
		stored.Status.Message.Parts[0].TextContent() != "first" {
		t.Fatalf("failed commit changed stored status: %+v", stored.Status)
	}
	history, err := manager.getConversationHistory(context.Background(), "", "", stored.ContextID, 100)
	if err != nil {
		t.Fatalf("get conversation history: %v", err)
	}
	for _, message := range history {
		if message.Role == protocol.MessageRoleAgent {
			t.Fatalf("failed commit advanced status history: %+v", message)
		}
	}
}

// Lua PX arguments must retain the same one-millisecond lower bound that
// go-redis applies to ordinary SET expirations.
func TestCrossNode_SubMillisecondExpirationIsClamped(t *testing.T) {
	manager, _ := setupTest(
		t,
		scriptedExecutor(),
		WithExpireTime(time.Nanosecond),
	)
	if manager.expiration != time.Millisecond {
		t.Fatalf("expiration = %s, want %s", manager.expiration, time.Millisecond)
	}
	task := storedTask(t, manager, "task-short-ttl", "ctx-short-ttl", protocol.TaskStateWorking)
	update := statusEvent(protocol.TaskStateWorking, agentReply("working"))
	update.TaskID = task.ID
	update.ContextID = task.ContextID
	task.Status = update.Status
	if err := manager.commitTaskEvent(
		context.Background(), "", "",
		task, protocol.NewStreamResponseStatusUpdate(update), false); err != nil {
		t.Fatalf("commitTaskEvent with sub-millisecond configured TTL: %v", err)
	}
	if got, err := manager.client.XLen(context.Background(), streamKey("", "", task.ID)).Result(); err != nil || got != 1 {
		t.Fatalf("stream event missing after clamped TTL: xlen=%d err=%v", got, err)
	}
}

// The event-transport path preserves storeTask's push-registration TTL refresh
// without pulling the cross-slot push key into the atomic Task/event script.
func TestCrossNode_TaskCommitRefreshesPushConfigExpiration(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	task := storedTask(t, manager, "task-push-expire", "ctx-push-expire", protocol.TaskStateWorking)
	pushKey := pushNotificationKey("", "", task.ID)
	if err := manager.client.HSet(context.Background(), pushKey, "config", "value").Err(); err != nil {
		t.Fatalf("seed push config: %v", err)
	}
	if err := manager.client.Persist(context.Background(), pushKey).Err(); err != nil {
		t.Fatalf("remove push config expiration: %v", err)
	}
	update := statusEvent(protocol.TaskStateWorking, nil)
	update.TaskID = task.ID
	update.ContextID = task.ContextID
	task.Status = update.Status
	if err := manager.commitTaskEvent(
		context.Background(), "", "",
		task, protocol.NewStreamResponseStatusUpdate(update), false); err != nil {
		t.Fatalf("commitTaskEvent: %v", err)
	}
	if ttl, err := manager.client.TTL(context.Background(), pushKey).Result(); err != nil || ttl <= 0 {
		t.Fatalf("push config TTL was not refreshed: ttl=%s err=%v", ttl, err)
	}
}

// A resubscribe that finishes its storage read after Close must be rejected;
// it cannot add a tailer after Close has already waited for all admitted ones.
func TestCrossNode_CloseRejectsTailerAfterSnapshotRead(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-close-admission", "ctx-close-admission", protocol.TaskStateWorking)
	if _, _, err := nodeB.eventTransport.LoadTaskAndCursor(context.Background(), "", "", task.ID); err != nil {
		t.Fatalf("warm loadTaskAndCursor script: %v", err)
	}
	hook := &blockAfterCommandHook{
		name:    "evalsha",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseHook := func() { releaseOnce.Do(func() { close(hook.release) }) }
	defer releaseHook()
	nodeB.client.AddHook(hook)

	type result struct {
		stream <-chan protocol.StreamResponse
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		stream, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
		resultCh <- result{stream: stream, err: err}
	}()
	select {
	case <-hook.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("resubscribe did not reach the post-read hook")
	}

	closed := make(chan error, 1)
	go func() { closed <- nodeB.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a tailer that was not admitted")
	}
	releaseHook()
	select {
	case got := <-resultCh:
		if got.err == nil || got.stream != nil {
			t.Fatalf("resubscribe admitted after Close: stream=%v err=%v", got.stream, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resubscribe did not return after storage hook was released")
	}
}
