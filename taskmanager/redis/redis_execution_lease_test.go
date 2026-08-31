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
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2/internal/executionlease"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

type blockingReleaseBackend struct {
	executionlease.Backend
	committed chan struct{}
	resume    chan struct{}
	once      sync.Once
}

func (b *blockingReleaseBackend) CommitExecutionTaskEvent(
	ctx context.Context,
	tenant, owner string,
	runID string,
	task *protocol.Task,
	event protocol.StreamResponse,
	allowCreate bool,
	release bool,
) error {
	err := b.Backend.CommitExecutionTaskEvent(
		ctx, tenant, owner, runID, task, event, allowCreate, release,
	)
	if err == nil && release {
		b.once.Do(func() {
			close(b.committed)
			<-b.resume
		})
	}
	return err
}

func TestExecutionLeaseSingleWriterAndStaleCommitFence(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	defer manager.Close()
	ctx := context.Background()
	const taskID = "task-execution-fence"

	if _, acquired, _, err := manager.executionLease.AcquireExecution(
		ctx, "", "", taskID, "run-1",
	); err != nil || !acquired {
		t.Fatalf("AcquireExecution(run-1) = (%v, %v), want acquired", acquired, err)
	}
	if _, acquired, pending, err := manager.executionLease.AcquireExecution(
		ctx, "", "", taskID, "run-2",
	); err != nil || acquired || pending {
		t.Fatalf("AcquireExecution(run-2 active) = (%v, %v, %v), want active", acquired, pending, err)
	}
	if err := manager.executionLease.ReleaseExecution(ctx, "", "", taskID, "run-1"); err != nil {
		t.Fatalf("ReleaseExecution(run-1): %v", err)
	}
	if _, acquired, _, err := manager.executionLease.AcquireExecution(
		ctx, "", "", taskID, "run-2",
	); err != nil || !acquired {
		t.Fatalf("AcquireExecution(run-2) = (%v, %v), want acquired", acquired, err)
	}

	task := protocol.NewTask(taskID, "ctx-execution-fence")
	task.Status = protocol.TaskStatus{
		State:     protocol.TaskStateWorking,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	event := &protocol.TaskStatusUpdateEvent{
		TaskID: task.ID, ContextID: task.ContextID, Status: task.Status,
	}
	response := protocol.NewStreamResponseStatusUpdate(event)
	if err := manager.commitExecutionTaskEvent(
		ctx, "", "", "run-1", task, response, true, false,
	); !errors.Is(err, executionlease.ErrStale) {
		t.Fatalf("stale CommitExecutionTaskEvent error = %v, want stale", err)
	}
	if err := manager.commitExecutionTaskEvent(
		ctx, "", "", "run-2", task, response, true, false,
	); err != nil {
		t.Fatalf("owner CommitExecutionTaskEvent: %v", err)
	}
}

func TestExecutionCancelCommitsAndReleaseClearsLease(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	defer manager.Close()
	ctx := context.Background()
	task := storedTask(t, manager, "task-release-cancel", "ctx-release-cancel", protocol.TaskStateWorking)

	if _, acquired, _, err := manager.executionLease.AcquireExecution(
		ctx, "", "", task.ID, "run-owner",
	); err != nil || !acquired {
		t.Fatalf("AcquireExecution = (%v, %v), want acquired", acquired, err)
	}
	canceled, committed, err := manager.executionLease.RequestExecutionCancel(
		ctx, "", "", task.ID,
	)
	if err != nil || !committed {
		t.Fatalf("RequestExecutionCancel = (%v, %v), want committed", committed, err)
	}
	if canceled == nil || canceled.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("canceled task = %+v, want CANCELED", canceled)
	}

	task.Status.State = protocol.TaskStateWorking
	event := &protocol.TaskStatusUpdateEvent{
		TaskID: task.ID, ContextID: task.ContextID, Status: task.Status,
	}
	if err := manager.commitExecutionTaskEvent(
		ctx, "", "", "run-owner", task,
		protocol.NewStreamResponseStatusUpdate(event), false, false,
	); !errors.Is(err, executionlease.ErrCancelRequested) {
		t.Fatalf("owner write after cancel error = %v, want cancel requested", err)
	}
	if err := manager.executionLease.ReleaseExecution(ctx, "", "", task.ID, "run-owner"); err != nil {
		t.Fatalf("ReleaseExecution: %v", err)
	}

	if exists, err := manager.client.Exists(ctx, executionKey("", "", task.ID)).Result(); err != nil || exists != 0 {
		t.Fatalf("execution record after release = %d, %v; want absent", exists, err)
	}
	terminal, acquired, pending, err := manager.executionLease.AcquireExecution(
		ctx, "", "", task.ID, "run-next",
	)
	if err != nil || acquired || pending || terminal == nil ||
		terminal.Status.State != protocol.TaskStateCanceled {
		t.Fatalf(
			"AcquireExecution after cancel = (%+v, %v, %v, %v), want terminal CANCELED",
			terminal, acquired, pending, err,
		)
	}
}

func TestReleaseCommitDoesNotRaceLeaseSweep(t *testing.T) {
	tests := []struct {
		name  string
		state protocol.TaskState
	}{
		{name: "terminal", state: protocol.TaskStateCompleted},
		{name: "suspended", state: protocol.TaskStateInputRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			emitRelease := make(chan struct{})
			processor := executorFunc(func(
				_ context.Context, _ *taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				out := make(chan protocol.StreamEvent, 2)
				go func() {
					defer close(out)
					out <- statusEvent(protocol.TaskStateWorking, nil)
					<-emitRelease
					out <- statusEvent(test.state, nil)
				}()
				return out, nil
			})
			manager, _ := setupTest(t, processor)
			backend := &blockingReleaseBackend{
				Backend:   manager.executionLease,
				committed: make(chan struct{}),
				resume:    make(chan struct{}),
			}
			manager.executionLease = backend
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(backend.resume) }) }
			t.Cleanup(func() {
				resume()
				_ = manager.Close()
			})

			stream, err := manager.OnSendMessageStream(
				context.Background(), sendParams("start", "ctx-release-"+test.name),
			)
			if err != nil {
				t.Fatalf("OnSendMessageStream: %v", err)
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

			close(emitRelease)
			select {
			case <-backend.committed:
			case <-time.After(2 * time.Second):
				t.Fatal("release commit did not reach Redis")
			}

			stored, err := manager.OnGetTask(
				context.Background(), protocol.TaskQueryParams{ID: working.TaskID},
			)
			if err != nil {
				t.Fatalf("OnGetTask after release commit: %v", err)
			}
			if stored.Status.State != test.state {
				t.Fatalf("stored state = %s, want %s", stored.Status.State, test.state)
			}

			// The Redis record is already gone while the engine is still waiting
			// for its script result. A sweep must recognize the intentional release
			// and leave the request-local stream open for the committed frame.
			manager.sweepExecutions()
			select {
			case _, ok := <-stream:
				if !ok {
					t.Fatal("lease sweep closed stream before release commit returned")
				}
				t.Fatal("release frame arrived before backend resumed")
			default:
			}

			resume()
			releaseFrame := recvEvent(t, stream)
			update := releaseFrame.GetStatusUpdate()
			if update == nil || update.Status.State != test.state {
				t.Fatalf("release frame = %+v, want %s", update, test.state)
			}
			if _, ok := <-stream; ok {
				t.Fatal("stream remained open after release frame")
			}
		})
	}
}
