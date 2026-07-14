// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// twoNodeManagers builds two RedisTaskManagers sharing one miniredis, both with
// cross-node resubscribe streaming enabled: nodeA runs procA, nodeB is a
// bystander instance a resubscribe may land on.
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
	managerOpts := append([]TaskManagerOption{WithCrossNodeResubscribe(true)}, opts...)
	a, err := NewTaskManager(procA, ca, managerOpts...)
	if err != nil {
		t.Fatalf("nodeA NewTaskManager: %v", err)
	}
	b, err := NewTaskManager(scriptedExecutor(), cb, managerOpts...)
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
	if got, err := nodeA.client.XLen(context.Background(), streamKey(taskA.ID)).Result(); err != nil || got != 2 {
		t.Fatalf("each status must be appended exactly once: xlen=%d err=%v", got, err)
	}
}

// An event published right after the atomic snapshot/cursor read, before the
// tailer's first XREAD, is still delivered.
func TestCrossNode_DeliversEventPublishedRightAfterResubscribe(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-gap", "ctx-gap", protocol.TaskStateWorking)
	if err := nodeA.appendTaskEvent(context.Background(), task.ID, protocol.NewStreamResponseStatusUpdate(
		statusEvent(protocol.TaskStateWorking, agentReply("w1")))); err != nil {
		t.Fatalf("append working event: %v", err)
	}

	chB, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	// Publish the terminal event immediately after resubscribe.
	if err := nodeA.appendTaskEvent(context.Background(), task.ID, protocol.NewStreamResponseStatusUpdate(
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

// With cross-node resubscribe disabled (default), no per-task stream key is
// created and the single-node resubscribe path is unchanged.
func TestCrossNodeResubscribe_Disabled_NoStreamKey(t *testing.T) {
	mgr, mr := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, agentReply("w")),
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	))
	if _, err := mgr.OnSendMessage(context.Background(), sendParams("go", "ctx")); err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, streamPrefix) {
			t.Fatalf("stream key %q created while cross-node resubscribe is off", k)
		}
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

// Close returns promptly and joins the tailer even with a resubscribe parked in
// a blocking XREAD.
func TestCrossNode_CloseJoinsActiveTailer(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-close", "ctx-close", protocol.TaskStateWorking)
	if _, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID}); err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	// nodeB's tailer is parked in XREAD BLOCK; Close must cancel and join it.
	done := make(chan error, 1)
	go func() { done <- nodeB.Close() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with an active resubscribe tailer")
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
		context.Background(), task, protocol.NewStreamResponseArtifactUpdate(first),
	); err != nil {
		t.Fatalf("store first task event: %v", err)
	}

	snapshot, cursor, err := nodeB.eventTransport.LoadTaskAndCursor(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("loadTaskAndCursor: %v", err)
	}
	if len(snapshot.Artifacts) != 1 {
		t.Fatalf("snapshot must contain the committed artifact exactly once, got %+v", snapshot)
	}
	if duplicate, err := nodeB.client.XRead(context.Background(), &redis.XReadArgs{
		Streams: []string{streamKey(task.ID), cursor},
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
		context.Background(), task, protocol.NewStreamResponseArtifactUpdate(second),
	); err != nil {
		t.Fatalf("store second task event: %v", err)
	}
	streams, err := nodeB.client.XRead(context.Background(), &redis.XReadArgs{
		Streams: []string{streamKey(task.ID), cursor},
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
		context.Background(), task, protocol.NewStreamResponseStatusUpdate(input),
	); err != nil {
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
		context.Background(), task, protocol.NewStreamResponseStatusUpdate(completed),
	); err != nil {
		t.Fatalf("store completed: %v", err)
	}
	if !drainForState(t, stream, protocol.TaskStateCompleted) {
		t.Fatal("resubscribe did not receive the terminal event after input-required")
	}
}

// The Redis tailer is an independent reader, so it uses cancelable blocking
// backpressure instead of treating a temporarily full output buffer as EOF.
func TestCrossNode_SlowConsumerDoesNotLoseStream(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(
		t,
		scriptedExecutor(),
		WithTaskSubscriberBufferSize(1),
	)
	task := storedTask(t, nodeA, "task-slow", "ctx-slow", protocol.TaskStateWorking)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := nodeB.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	const updates = 4
	for i := 0; i < updates; i++ {
		update := protocol.NewStreamResponseStatusUpdate(
			statusEvent(protocol.TaskStateWorking, agentReply("still working")),
		)
		if err := nodeA.appendTaskEvent(context.Background(), task.ID, update); err != nil {
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
		// Keep the size-one buffer full between reads. A non-blocking tailer would
		// evict the subscriber before all queued Redis events are delivered.
		time.Sleep(10 * time.Millisecond)
	}
}

// Storage/cursor errors are surfaced. A malformed stream key must neither be
// treated as an empty stream nor allow a Task update to commit by itself.
func TestCrossNode_WrongTypeStreamFailsAtomically(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-wrongtype", "ctx-wrongtype", protocol.TaskStateWorking)
	if err := nodeA.client.Set(context.Background(), streamKey(task.ID), "not-a-stream", 0).Err(); err != nil {
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

// A Message emitted for an existing Task is not reported to the RPC or local
// subscribers unless its cross-node stream append succeeds.
func TestCrossNode_MessageAppendFailureIsNotExposedAsSuccess(t *testing.T) {
	manager, _ := setupTest(
		t,
		scriptedExecutor(agentReply("reply")),
		WithCrossNodeResubscribe(true),
	)
	task := storedTask(t, manager, "task-message-fail", "ctx-message-fail", protocol.TaskStateWorking)
	if err := manager.client.Set(context.Background(), streamKey(task.ID), "not-a-stream", 0).Err(); err != nil {
		t.Fatalf("seed wrong-type stream key: %v", err)
	}
	subscriber := newTaskSubscriber(task.ID, 1, false)
	manager.subMu.Lock()
	manager.subscribers[task.ID] = []*taskSubscriber{subscriber}
	manager.subMu.Unlock()
	defer manager.cleanupFailedSubscribers(task.ID, []*taskSubscriber{subscriber})

	params := sendParams("continue", task.ContextID)
	params.Message.TaskID = &task.ID
	if response, err := manager.OnSendMessage(context.Background(), params); err == nil {
		t.Fatalf("message append failure was exposed as success: %+v", response)
	}
	if buffered := len(subscriber.Channel()); buffered != 0 {
		t.Fatalf("message append failure reached local subscriber: buffered=%d", buffered)
	}
}

// Lua PX arguments must retain the same one-millisecond lower bound that
// go-redis applies to ordinary SET expirations.
func TestCrossNode_SubMillisecondExpirationIsClamped(t *testing.T) {
	manager, _ := setupTest(
		t,
		scriptedExecutor(),
		WithCrossNodeResubscribe(true),
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
		context.Background(), task, protocol.NewStreamResponseStatusUpdate(update),
	); err != nil {
		t.Fatalf("commitTaskEvent with sub-millisecond configured TTL: %v", err)
	}
	if got, err := manager.client.XLen(context.Background(), streamKey(task.ID)).Result(); err != nil || got != 1 {
		t.Fatalf("stream event missing after clamped TTL: xlen=%d err=%v", got, err)
	}
}

// A resubscribe that finishes its storage read after Close must be rejected;
// it cannot add a tailer after Close has already waited for all admitted ones.
func TestCrossNode_CloseRejectsTailerAfterSnapshotRead(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-close-admission", "ctx-close-admission", protocol.TaskStateWorking)
	if _, _, err := nodeB.eventTransport.LoadTaskAndCursor(context.Background(), task.ID); err != nil {
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
