// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2026 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2/internal/executionlease"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestCommitTaskEventStateArgumentPreservesLeaseSemantics(t *testing.T) {
	tests := []struct {
		name                 string
		nextState            protocol.TaskState
		terminalCanWinCancel bool
		wantCancelError      bool
	}{
		{name: "canceled wins cancellation", nextState: protocol.TaskStateCanceled},
		{name: "completed cannot win by default", nextState: protocol.TaskStateCompleted, wantCancelError: true},
		{name: "completed may win when allowed", nextState: protocol.TaskStateCompleted, terminalCanWinCancel: true},
	}
	paths := []struct {
		name       string
		legacyArgs bool
	}{
		{name: "go transport"},
		{name: "legacy 12 args", legacyArgs: true},
	}

	for _, path := range paths {
		for _, tt := range tests {
			t.Run(path.name+"/"+tt.name, func(t *testing.T) {
				manager, _ := setupTest(t, scriptedExecutor())
				defer manager.Close()

				ctx := context.Background()
				transport := manager.eventTransport.(*redisTaskEventTransport)
				const taskID = "task-state-argument-lease"
				const runID = "run-state-argument-lease"
				task := storedTask(t, manager, taskID, "ctx-state-argument-lease", protocol.TaskStateWorking)
				if _, acquired, _, err := transport.AcquireExecution(ctx, "", "", taskID, runID); err != nil || !acquired {
					t.Fatalf("AcquireExecution = (acquired=%v, err=%v), want acquired", acquired, err)
				}
				if err := transport.client.HSet(ctx, executionKey("", "", taskID), "cancel_requested", "1").Err(); err != nil {
					t.Fatalf("set cancellation intent: %v", err)
				}

				nextTask := *task
				nextTask.Status.State = tt.nextState
				statusEvent := &protocol.TaskStatusUpdateEvent{
					TaskID: taskID, ContextID: task.ContextID, Status: nextTask.Status,
				}
				event := protocol.NewStreamResponseStatusUpdate(statusEvent)
				var err error
				if path.legacyArgs {
					err = commitTaskEventWithLegacyArgs(ctx, transport, &nextTask, event, runID, tt.terminalCanWinCancel)
				} else {
					err = transport.CommitExecutionTaskEvent(
						ctx, "", "", runID, &nextTask, event, false, false, tt.terminalCanWinCancel,
					)
				}

				if tt.wantCancelError {
					if path.legacyArgs {
						if err == nil || !strings.Contains(err.Error(), "EXECUTION_CANCEL_REQUESTED") {
							t.Fatalf("commit error = %v, want cancellation rejection", err)
						}
					} else if !errors.Is(err, executionlease.ErrCancelRequested) {
						t.Fatalf("commit error = %v, want ErrCancelRequested", err)
					}
				} else if err != nil {
					t.Fatalf("commit: %v", err)
				}

				leaseExists, err := transport.client.Exists(ctx, executionKey("", "", taskID)).Result()
				if err != nil {
					t.Fatalf("check execution lease: %v", err)
				}
				wantLease := int64(0)
				wantState := tt.nextState
				if tt.wantCancelError {
					wantLease = 1
					wantState = protocol.TaskStateWorking
				}
				if leaseExists != wantLease {
					t.Fatalf("execution lease exists = %d, want %d", leaseExists, wantLease)
				}
				stored, getErr := manager.getTaskInternal(ctx, "", "", taskID)
				if getErr != nil {
					t.Fatalf("get task after commit: %v", getErr)
				}
				if stored.Status.State != wantState {
					t.Fatalf("stored state = %s, want %s", stored.Status.State, wantState)
				}
			})
		}
	}
}

// commitTaskEventWithLegacyArgs calls the script with its original 12 ARGV
// values to guard the decode fallback when the state argument is omitted.
func commitTaskEventWithLegacyArgs(
	ctx context.Context,
	transport *redisTaskEventTransport,
	task *protocol.Task,
	event protocol.StreamResponse,
	runID string,
	terminalCanWinCancel bool,
) error {
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return err
	}
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return err
	}
	terminalOverride := 0
	if terminalCanWinCancel {
		terminalOverride = 1
	}
	_, err = commitTaskEventScript.Run(
		ctx,
		transport.client,
		[]string{
			taskKey("", "", task.ID),
			streamKey("", "", task.ID),
			streamDedupeKey("", "", task.ID),
			executionKey("", "", task.ID),
			snapshotCursorKey("", "", task.ID),
		},
		taskBytes,
		eventBytes,
		transport.streamMaxLen,
		streamField,
		transport.expiration.Milliseconds(),
		0,
		"legacy-state-argument-operation",
		runID,
		transport.executionLeaseDuration.Milliseconds(),
		0,
		transport.executionRetention.Milliseconds(),
		terminalOverride,
	).Result()
	return err
}
