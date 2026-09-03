// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redisclient "github.com/redis/go-redis/v9"

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

type mutateOnGetClient struct {
	redisclient.UniversalClient
	target string
	mutate func(context.Context, []byte)
}

// Get returns the observed Task and then injects a concurrent owner commit.
func (c *mutateOnGetClient) Get(ctx context.Context, key string) *redisclient.StringCmd {
	cmd := c.UniversalClient.Get(ctx, key)
	if key == c.target && cmd.Err() == nil {
		c.mutate(ctx, []byte(cmd.Val()))
	}
	return cmd
}

type blockingRenewBackend struct {
	executionlease.Backend
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	active  int
	max     int
}

type blockingIndexClient struct {
	redisclient.UniversalClient
	entered chan struct{}
	resume  chan struct{}
	blocked atomic.Bool
}

func (c *blockingIndexClient) TxPipelined(
	ctx context.Context,
	fn func(redisclient.Pipeliner) error,
) ([]redisclient.Cmder, error) {
	if c.blocked.CompareAndSwap(false, true) {
		close(c.entered)
		<-c.resume
	}
	return c.UniversalClient.TxPipelined(ctx, fn)
}

// CheckAndRenewExecution blocks so the test can observe sweep parallelism.
func (b *blockingRenewBackend) CheckAndRenewExecution(
	context.Context, string, string, string, string,
) (bool, bool, error) {
	b.mu.Lock()
	b.active++
	if b.active > b.max {
		b.max = b.active
	}
	b.mu.Unlock()
	b.started <- struct{}{}
	<-b.release
	b.mu.Lock()
	b.active--
	b.mu.Unlock()
	return true, false, nil
}

// CommitExecutionTaskEvent blocks a successful lease-releasing commit.
func (b *blockingReleaseBackend) CommitExecutionTaskEvent(
	ctx context.Context,
	tenant, owner string,
	runID string,
	task *protocol.Task,
	event protocol.StreamResponse,
	allowCreate bool,
	release bool,
	terminalCanWinCancel bool,
) error {
	err := b.Backend.CommitExecutionTaskEvent(
		ctx, tenant, owner, runID, task, event, allowCreate, release, terminalCanWinCancel,
	)
	if err == nil && release {
		b.once.Do(func() {
			close(b.committed)
			<-b.resume
		})
	}
	return err
}

// TestExecutionLeaseSingleWriterAndStaleCommitFence verifies lease ownership fencing.
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
		ctx, "", "", "run-1", task, response, true, false, false,
	); !errors.Is(err, executionlease.ErrStale) {
		t.Fatalf("stale CommitExecutionTaskEvent error = %v, want stale", err)
	}
	if err := manager.commitExecutionTaskEvent(
		ctx, "", "", "run-2", task, response, true, false, false,
	); err != nil {
		t.Fatalf("owner CommitExecutionTaskEvent: %v", err)
	}
}

// TestExecutionCancelRecordsIntentAcrossRacingTaskUpdate verifies that a live
// owner's update cannot consume every cancellation CAS attempt.
func TestExecutionCancelRecordsIntentAcrossRacingTaskUpdate(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	defer manager.Close()
	ctx := context.Background()
	task := storedTask(t, manager, "task-cancel-race", "ctx-cancel-race", protocol.TaskStateWorking)
	const runID = "run-cancel-race"
	if _, acquired, _, err := manager.executionLease.AcquireExecution(
		ctx, "", "", task.ID, runID,
	); err != nil || !acquired {
		t.Fatalf("AcquireExecution = (%v, %v), want acquired", acquired, err)
	}

	original := manager.executionLease.(*redisTaskEventTransport)
	wrapped := *original
	var commits int
	var commitErr error
	wrapped.client = &mutateOnGetClient{
		UniversalClient: original.client,
		target:          taskKey("", "", task.ID),
		mutate: func(ctx context.Context, payload []byte) {
			var updated protocol.Task
			if err := json.Unmarshal(payload, &updated); err != nil {
				commitErr = err
				return
			}
			if updated.Metadata == nil {
				updated.Metadata = make(map[string]interface{})
			}
			updated.Metadata["revision"] = commits + 1
			event := &protocol.TaskStatusUpdateEvent{
				TaskID: updated.ID, ContextID: updated.ContextID, Status: updated.Status,
			}
			commitErr = manager.commitExecutionTaskEvent(
				ctx, "", "", runID, &updated,
				protocol.NewStreamResponseStatusUpdate(event), false, false, false,
			)
			if commitErr == nil {
				commits++
			}
		},
	}

	snapshot, committed, _, err := wrapped.RequestExecutionCancel(ctx, "", "", task.ID)
	if err != nil {
		t.Fatalf("RequestExecutionCancel: %v", err)
	}
	if committed {
		t.Fatal("live cancellation committed CANCELED instead of recording intent")
	}
	if snapshot == nil || snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("cancel snapshot = %+v, want current WORKING Task", snapshot)
	}
	if commitErr != nil || commits != 1 {
		t.Fatalf("racing owner commits = %d, error = %v; want one successful commit", commits, commitErr)
	}
	if got := manager.client.HGet(ctx, executionKey("", "", task.ID), "cancel_requested").Val(); got != "1" {
		t.Fatalf("cancel_requested = %q, want 1", got)
	}
}

// TestExecutionSweepRenewsConcurrentlyAndWithinBound verifies that one slow
// Redis round trip does not serialize every live execution renewal.
func TestExecutionSweepRenewsConcurrentlyAndWithinBound(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	manager.controlCancel()
	manager.controlWg.Wait()
	manager.controlCtx, manager.controlCancel = context.WithCancel(context.Background())
	defer manager.Close()

	runs := executionRenewalConcurrency + 3
	backend := &blockingRenewBackend{
		Backend: manager.executionLease,
		started: make(chan struct{}, runs),
		release: make(chan struct{}),
	}
	manager.executionLease = backend
	manager.cancelMu.Lock()
	for i := 0; i < runs; i++ {
		key := newScopedID("", "", fmt.Sprintf("task-renew-%d", i))
		live := &liveExecution{cancel: func() {}, runID: fmt.Sprintf("run-renew-%d", i)}
		live.leaseUntilMillis.Store(time.Now().Add(time.Minute).UnixMilli())
		manager.executions[key] = live
	}
	manager.cancelMu.Unlock()

	done := make(chan struct{})
	go func() {
		manager.sweepExecutions()
		close(done)
	}()
	for i := 0; i < executionRenewalConcurrency; i++ {
		select {
		case <-backend.started:
		case <-time.After(time.Second):
			t.Fatalf("only %d renewals started concurrently", i)
		}
	}
	select {
	case <-backend.started:
		t.Fatalf("renewal concurrency exceeded %d", executionRenewalConcurrency)
	case <-time.After(20 * time.Millisecond):
	}
	close(backend.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent renewal sweep did not finish")
	}
	backend.mu.Lock()
	maxActive := backend.max
	backend.mu.Unlock()
	if maxActive != executionRenewalConcurrency {
		t.Fatalf("maximum concurrent renewals = %d, want %d", maxActive, executionRenewalConcurrency)
	}
	manager.cancelMu.Lock()
	for key := range manager.executions {
		delete(manager.executions, key)
	}
	manager.cancelMu.Unlock()
}

// TestExecutionCancelRecordsIntentAndLateTerminalWins verifies the owner may
// still commit a terminal result after cancellation intent is recorded.
func TestExecutionCancelRecordsIntentAndLateTerminalWins(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	defer manager.Close()
	ctx := context.Background()
	task := storedTask(t, manager, "task-release-cancel", "ctx-release-cancel", protocol.TaskStateWorking)

	if _, acquired, _, err := manager.executionLease.AcquireExecution(
		ctx, "", "", task.ID, "run-owner",
	); err != nil || !acquired {
		t.Fatalf("AcquireExecution = (%v, %v), want acquired", acquired, err)
	}
	snapshot, committed, _, err := manager.executionLease.RequestExecutionCancel(
		ctx, "", "", task.ID,
	)
	if err != nil || committed {
		t.Fatalf("RequestExecutionCancel = (%v, %v), want intent only", committed, err)
	}
	if snapshot == nil || snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("cancel snapshot = %+v, want current WORKING Task", snapshot)
	}

	task.Status.State = protocol.TaskStateWorking
	event := &protocol.TaskStatusUpdateEvent{
		TaskID: task.ID, ContextID: task.ContextID, Status: task.Status,
	}
	if err := manager.commitExecutionTaskEvent(
		ctx, "", "", "run-owner", task,
		protocol.NewStreamResponseStatusUpdate(event), false, false, false,
	); !errors.Is(err, executionlease.ErrCancelRequested) {
		t.Fatalf("non-terminal owner write after cancel error = %v, want cancel requested", err)
	}

	task.Status.State = protocol.TaskStateCompleted
	event.Status = task.Status
	event.Final = true
	if err := manager.commitExecutionTaskEvent(
		ctx, "", "", "run-owner", task,
		protocol.NewStreamResponseStatusUpdate(event), false, true, true,
	); err != nil {
		t.Fatalf("late terminal owner write after cancel: %v", err)
	}

	if exists, err := manager.client.Exists(ctx, executionKey("", "", task.ID)).Result(); err != nil || exists != 0 {
		t.Fatalf("execution record after release = %d, %v; want absent", exists, err)
	}
	terminal, acquired, pending, err := manager.executionLease.AcquireExecution(
		ctx, "", "", task.ID, "run-next",
	)
	if err != nil || acquired || pending || terminal == nil ||
		terminal.Status.State != protocol.TaskStateCompleted {
		t.Fatalf(
			"AcquireExecution after cancel = (%+v, %v, %v, %v), want terminal COMPLETED",
			terminal, acquired, pending, err,
		)
	}
}

func TestExecutionCancelRequiresOwnerAcknowledgement(t *testing.T) {
	manager, _ := setupTest(t, scriptedExecutor())
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	task := storedTask(t, manager, "task-cancel-ack", "ctx-cancel-ack", protocol.TaskStateWorking)
	if _, acquired, _, err := manager.executionLease.AcquireExecution(
		ctx, "", "", task.ID, "run-owner",
	); err != nil || !acquired {
		t.Fatalf("AcquireExecution = (%v, %v), want acquired", acquired, err)
	}

	snapshot, committed, acknowledged, err := manager.executionLease.RequestExecutionCancel(
		ctx, "", "", task.ID,
	)
	if err != nil || committed || acknowledged {
		t.Fatalf("first cancel = (%+v, %v, %v, %v), want pending intent", snapshot, committed, acknowledged, err)
	}
	if err := manager.executionLease.AcknowledgeExecutionCancel(
		ctx, "", "", task.ID, "run-owner",
	); err != nil {
		t.Fatalf("AcknowledgeExecutionCancel: %v", err)
	}
	snapshot, committed, acknowledged, err = manager.executionLease.RequestExecutionCancel(
		ctx, "", "", task.ID,
	)
	if err != nil || committed || !acknowledged || snapshot == nil ||
		snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("acknowledged cancel = (%+v, %v, %v, %v), want acknowledged WORKING", snapshot, committed, acknowledged, err)
	}
}

// TestReleaseCommitDoesNotRaceLeaseSweep verifies intentional release is not treated as lease loss.
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

// TestTerminalPreparationKeepsRenewing verifies that cross-slot index work
// before the fenced terminal commit does not open a takeover window.
func TestTerminalPreparationKeepsRenewing(t *testing.T) {
	emitTerminal := make(chan struct{})
	processor := executorFunc(func(
		_ context.Context, _ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-emitTerminal
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	manager, redisServer := setupTest(t, processor, WithExpireTime(600*time.Millisecond))
	manager.controlCancel()
	manager.controlWg.Wait()
	manager.controlCtx, manager.controlCancel = context.WithCancel(context.Background())
	t.Cleanup(func() { _ = manager.Close() })
	baseTime := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	redisServer.SetTime(baseTime)

	stream, err := manager.OnSendMessageStream(context.Background(), sendParams("start", "ctx-terminal-renew"))
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

	blocked := &blockingIndexClient{
		UniversalClient: manager.client,
		entered:         make(chan struct{}),
		resume:          make(chan struct{}),
	}
	manager.client = blocked
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(blocked.resume) }) }
	t.Cleanup(resume)
	close(emitTerminal)
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal event did not block before the fenced commit")
	}

	redisServer.SetTime(baseTime.Add(200 * time.Millisecond))
	manager.sweepExecutions()
	redisServer.SetTime(baseTime.Add(400 * time.Millisecond))

	otherClient := redisclient.NewClient(&redisclient.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = otherClient.Close() })
	other, err := NewTaskManager(scriptedExecutor(), otherClient, WithExpireTime(600*time.Millisecond))
	if err != nil {
		t.Fatalf("second NewTaskManager: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	_, acquired, _, err := other.executionLease.AcquireExecution(
		context.Background(), "", "", working.TaskID, "run-takeover",
	)
	if err != nil {
		t.Fatalf("second AcquireExecution: %v", err)
	}
	if acquired {
		t.Fatal("second node acquired while terminal event was still pre-commit")
	}

	resume()
	completedFrame := recvEvent(t, stream)
	completed := completedFrame.GetStatusUpdate()
	if completed == nil || completed.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("terminal frame = %+v, want COMPLETED", completed)
	}
	if _, ok := <-stream; ok {
		t.Fatal("stream remained open after terminal frame")
	}
	stored, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: working.TaskID})
	if err != nil || stored.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("stored task = %+v, err=%v; want COMPLETED", stored, err)
	}
}
