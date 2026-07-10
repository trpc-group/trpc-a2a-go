// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"strings"
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
func twoNodeManagers(t *testing.T, procA taskmanager.MessageProcessor) (*TaskManager, *TaskManager) {
	t.Helper()
	mr := miniredis.RunT(t)
	ca := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { ca.Close(); cb.Close() })
	a, err := NewTaskManager(procA, ca, WithResubscribeStreaming(true))
	if err != nil {
		t.Fatalf("nodeA NewTaskManager: %v", err)
	}
	b, err := NewTaskManager(scriptedExecutor(), cb, WithResubscribeStreaming(true))
	if err != nil {
		t.Fatalf("nodeB NewTaskManager: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
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
// receives the current snapshot and every subsequent event via the shared Redis
// stream, and its stream closes on the terminal frame.
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
}

// An event published right after resubscribe (in the window between the cursor
// capture and the tailer's first read) is still delivered. This guards the
// synchronous pre-snapshot cursor against the gap a "$" cursor would leave.
func TestCrossNode_DeliversEventPublishedRightAfterResubscribe(t *testing.T) {
	nodeA, nodeB := twoNodeManagers(t, scriptedExecutor())
	task := storedTask(t, nodeA, "task-gap", "ctx-gap", protocol.TaskStateWorking)
	nodeA.publishStream(task.ID, protocol.NewStreamResponseStatusUpdate(
		statusEvent(protocol.TaskStateWorking, agentReply("w1"))))

	chB, err := nodeB.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	// Publish the terminal event immediately after resubscribe.
	nodeA.publishStream(task.ID, protocol.NewStreamResponseStatusUpdate(
		statusEvent(protocol.TaskStateCompleted, agentReply("done"))))

	first, _ := recvTimeout(t, chB)
	if first.GetTask() == nil {
		t.Fatalf("first frame must be the snapshot, got %+v", first)
	}
	if !drainForState(t, chB, protocol.TaskStateCompleted) {
		t.Fatal("resubscriber lost the event published right after resubscribe (cursor gap)")
	}
}

// A cancel of a task with no live run persists CANCELED and notifies via
// notifySubscribers, bypassing broadcast. The stream seam sits at
// notifySubscribers, so a cross-node resubscriber still sees the CANCELED frame.
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

// With streaming disabled (default) no per-task stream key is created and the
// resubscribe path is unchanged.
func TestResubscribeStreaming_Disabled_NoStreamKey(t *testing.T) {
	mgr, mr := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, agentReply("w")),
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	))
	if _, err := mgr.OnSendMessage(context.Background(), sendParams("go", "ctx")); err != nil {
		t.Fatalf("OnSendMessage: %v", err)
	}
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, streamPrefix) {
			t.Fatalf("stream key %q created while ResubscribeStreaming is off", k)
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
