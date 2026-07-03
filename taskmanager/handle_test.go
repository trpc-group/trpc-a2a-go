// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// A handle emit blocked on a full buffer (the classic mis-ported synchronous
// processor) must abort with the ctx error once the round is canceled, instead
// of deadlocking forever.
func TestTaskHandle_BlockedEmitUnblocksOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewTaskHandle(ctx, &ExecContext{TaskID: "task-1"}, 0)

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.UpdateTaskState(protocol.TaskStateWorking, nil)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("emit with no consumer should block, returned %v", err)
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
func TestTaskHandle_BufferedEmitSucceedsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := NewTaskHandle(ctx, &ExecContext{TaskID: "task-1"}, 1)

	if err := h.UpdateTaskState(protocol.TaskStateCompleted, ReplyText("done")); err != nil {
		t.Fatalf("buffered emit after cancel must succeed (terminal wins), got %v", err)
	}
}
