// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// The migration guarantee: a fully synchronous v0.x-style body — any number of
// emits from the ProcessMessage goroutine itself, before Events() — never
// blocks, and Events() then yields every event in order.
func TestTaskHandle_SynchronousBodyNeverBlocks(t *testing.T) {
	const n = 10000
	h := NewTaskHandle(context.Background(), &ExecContext{TaskID: "task-1"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			if err := h.UpdateTaskState(protocol.TaskStateWorking, nil); err != nil {
				t.Errorf("pre-Events emit %d failed: %v", i, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous emits blocked before Events()")
	}

	events := h.Events()
	h.Close()
	count := 0
	for range events {
		count++
	}
	if count != n {
		t.Fatalf("expected %d events after flush, got %d", n, count)
	}
}

// Emits made before Events() and after it arrive in emission order.
func TestTaskHandle_FlushPreservesOrder(t *testing.T) {
	h := NewTaskHandle(context.Background(), &ExecContext{TaskID: "task-1"})

	if err := h.UpdateTaskState(protocol.TaskStateWorking, nil); err != nil {
		t.Fatalf("queued emit failed: %v", err)
	}
	if err := h.AddArtifact(protocol.Artifact{ArtifactID: "art-1"}, false); err != nil {
		t.Fatalf("queued emit failed: %v", err)
	}
	events := h.Events()
	if err := h.UpdateTaskState(protocol.TaskStateCompleted, nil); err != nil {
		t.Fatalf("live emit failed: %v", err)
	}
	h.Close()

	var kinds []string
	for event := range events {
		switch event.(type) {
		case *protocol.TaskStatusUpdateEvent:
			kinds = append(kinds, "status")
		case *protocol.TaskArtifactUpdateEvent:
			kinds = append(kinds, "artifact")
		default:
			kinds = append(kinds, "other")
		}
	}
	want := []string{"status", "artifact", "status"}
	if len(kinds) != len(want) {
		t.Fatalf("expected %v, got %v", want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, kinds)
		}
	}
}

// The sync pattern from the type doc: defer h.Close() runs after the return
// value (h.Events()) is evaluated — the framework sees the events, then the
// end of the round.
func TestTaskHandle_DeferClosePattern(t *testing.T) {
	round := func() <-chan protocol.StreamEvent {
		h := NewTaskHandle(context.Background(), &ExecContext{TaskID: "task-1"})
		defer h.Close()
		h.UpdateTaskState(protocol.TaskStateWorking, nil)
		h.UpdateTaskState(protocol.TaskStateCompleted, ReplyText("done"))
		return h.Events()
	}

	count := 0
	for range round() {
		count++
	}
	if count != 2 {
		t.Fatalf("expected 2 events then close, got %d", count)
	}
}

// Close is idempotent, and an emit after Close fails with an error instead of
// panicking (the v0.x TaskHandler verbs also reported errors).
func TestTaskHandle_EmitAfterCloseErrors(t *testing.T) {
	h := NewTaskHandle(context.Background(), &ExecContext{TaskID: "task-1"})
	h.Close()
	h.Close() // idempotent

	if err := h.UpdateTaskState(protocol.TaskStateWorking, nil); !errors.Is(err, errRoundClosed) {
		t.Fatalf("expected errRoundClosed after Close, got %v", err)
	}

	// Closing before Events(): the returned channel is already closed.
	if _, ok := <-h.Events(); ok {
		t.Fatal("expected a closed events channel after Close")
	}
}

// A live emit blocked on the full channel (a mis-ported emitter racing no
// consumer) must abort with the ctx error once the round is canceled, instead
// of deadlocking forever.
func TestTaskHandle_BlockedLiveEmitUnblocksOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewTaskHandle(ctx, &ExecContext{TaskID: "task-1"})
	h.Events() // switch to live mode; nobody drains

	errCh := make(chan error, 1)
	go func() {
		for {
			if err := h.UpdateTaskState(protocol.TaskStateWorking, nil); err != nil {
				errCh <- err
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		t.Fatalf("live emits with no consumer should fill the buffer and block, returned %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected the ctx error from the aborted emit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("emit still blocked after the round was canceled")
	}
}

// An emit that CAN proceed still does after cancellation: a post-cancel
// terminal event must reach the framework (its terminal state wins per the
// contract), so only sends with no consumer fail.
func TestTaskHandle_EmitSucceedsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := NewTaskHandle(ctx, &ExecContext{TaskID: "task-1"})

	// Pre-Events emits always succeed, canceled or not.
	if err := h.UpdateTaskState(protocol.TaskStateCompleted, ReplyText("done")); err != nil {
		t.Fatalf("queued emit after cancel must succeed (terminal wins), got %v", err)
	}
	// Live emits with buffer space succeed too.
	h.Events()
	if err := h.UpdateTaskState(protocol.TaskStateCompleted, nil); err != nil {
		t.Fatalf("buffered live emit after cancel must succeed, got %v", err)
	}
}
