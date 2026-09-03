// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// executorFunc adapts a function to the taskmanager.MessageProcessor interface.
type executorFunc func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error)

// ProcessMessage implements taskmanager.MessageProcessor.
func (f executorFunc) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	return f(ctx, ec)
}

// scriptedExecutor returns an MessageProcessor that emits the given events in order
// and closes the channel.
func scriptedExecutor(events ...protocol.StreamEvent) executorFunc {
	return func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, len(events)+1)
		for _, event := range events {
			out <- event
		}
		close(out)
		return out, nil
	}
}

func setupTest(
	t *testing.T,
	processor taskmanager.MessageProcessor,
	opts ...TaskManagerOption,
) (*TaskManager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	manager, err := NewTaskManager(processor, client, opts...)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	return manager, mr
}

func agentReply(text string) *protocol.Message {
	msg := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart(text)})
	return &msg
}

func statusEvent(state protocol.TaskState, msg *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: state, Message: msg}}
}

func artifactEvent(artifactID, text string) *protocol.TaskArtifactUpdateEvent {
	lastChunk := true
	return &protocol.TaskArtifactUpdateEvent{
		Artifact: protocol.Artifact{
			ArtifactID: artifactID,
			Parts:      []*protocol.Part{protocol.NewTextPart(text)},
		},
		LastChunk: &lastChunk,
	}
}

func sendParams(text, contextID string) protocol.SendMessageParams {
	msg := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart(text)})
	if contextID != "" {
		msg.ContextID = &contextID
	}
	return protocol.SendMessageParams{Message: msg}
}

func storedTask(t *testing.T, m *TaskManager, id, contextID string, state protocol.TaskState) *protocol.Task {
	t.Helper()
	task := &protocol.Task{
		ID:        id,
		ContextID: contextID,
		Status: protocol.TaskStatus{
			State:     state,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := m.storeTask(context.Background(), "", "", task); err != nil {
		t.Fatalf("storeTask failed: %v", err)
	}
	return task
}

func waitTaskState(t *testing.T, m *TaskManager, taskID string, state protocol.TaskState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := m.getTaskInternal(context.Background(), "", "", taskID)
		if err == nil && task.Status.State == state {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach state %s in time", taskID, state)
}

// =============================================================================
// Constructor
// =============================================================================

func TestNewTaskManagerValidation(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	if _, err := NewTaskManager(nil, client); err == nil {
		t.Error("expected error for nil processor")
	}
	if _, err := NewTaskManager(scriptedExecutor(), nil); err == nil {
		t.Error("expected error for nil client")
	}

	// Unreachable Redis fails the constructor ping.
	deadClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond})
	defer deadClient.Close()
	if _, err := NewTaskManager(scriptedExecutor(), deadClient); err == nil {
		t.Error("expected error for unreachable redis")
	}
}

// =============================================================================
// message/send: unary flows
// =============================================================================

func TestOnSendMessagePureMessage(t *testing.T) {
	m, mr := setupTest(t, scriptedExecutor(agentReply("hi there")))

	resp, err := m.OnSendMessage(context.Background(), sendParams("hello", "ctx-pure"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	reply := resp.GetMessage()
	if reply == nil {
		t.Fatalf("expected message result, got %+v", resp.Result)
	}
	if got := reply.Parts[0].TextContent(); got != "hi there" {
		t.Errorf("unexpected reply text %q", got)
	}
	if reply.Role != protocol.MessageRoleAgent {
		t.Errorf("expected agent role, got %s", reply.Role)
	}

	// A pure-message exchange must not leave a task behind in Redis.
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, tenantKeyPrefix("")+taskPrefix) {
			t.Errorf("unexpected task key in redis: %s", key)
		}
	}

	// Both the user message and the reply live in the conversation.
	history, err := m.getConversationHistory(context.Background(), "", "", "ctx-pure", 10)
	if err != nil {
		t.Fatalf("getConversationHistory failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 conversation messages, got %d", len(history))
	}
	if history[1].Parts[0].TextContent() != "hi there" {
		t.Errorf("reply not stored in conversation")
	}
}

func TestOnSendMessageWorkingCompleted(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		statusEvent(protocol.TaskStateCompleted, agentReply("done")),
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("do it", "ctx-wc"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		t.Fatalf("expected task result, got %+v", resp.Result)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Errorf("expected completed, got %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.Parts[0].TextContent() != "done" {
		t.Errorf("expected status message 'done', got %+v", task.Status.Message)
	}
	if task.ContextID != "ctx-wc" {
		t.Errorf("expected stamped context ID, got %q", task.ContextID)
	}

	// GetTask agrees with the unary result (persisted before returned).
	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCompleted {
		t.Errorf("stored task state = %s, want completed", stored.Status.State)
	}
}

func TestSupersededStatusMessageMovesToHistory(t *testing.T) {
	working := agentReply("still working")
	done := agentReply("done")
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, working),
		statusEvent(protocol.TaskStateCompleted, done),
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("do it", "ctx-status-history"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil || task.Status.Message == nil {
		t.Fatalf("expected a task with a current status message, got %+v", resp.Result)
	}
	if len(task.History) != 2 {
		t.Fatalf("expected user + superseded status message in history, got %d", len(task.History))
	}
	if task.History[1].MessageID != working.MessageID {
		t.Errorf("expected the superseded working message in history, got %s", task.History[1].MessageID)
	}
	for _, message := range task.History {
		if message.MessageID == task.Status.Message.MessageID {
			t.Fatalf("current status message %s must not also appear in history", message.MessageID)
		}
	}
}

func TestOnSendMessageHistoryLength(t *testing.T) {
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateCompleted, nil)
		close(out)
		return out, nil
	})
	m, _ := setupTest(t, processor)
	contextID := "ctx-hist"
	for _, text := range []string{"m1", "m2"} {
		msg := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart(text)})
		msg.ContextID = &contextID
		m.storeMessage(context.Background(), "", "", msg)
	}

	send := func(text string, historyLength *int) *protocol.Task {
		t.Helper()
		params := sendParams(text, contextID)
		if historyLength != nil {
			params.Configuration = &protocol.SendMessageConfiguration{HistoryLength: historyLength}
		}
		resp, err := m.OnSendMessage(context.Background(), params)
		if err != nil {
			t.Fatalf("OnSendMessage failed: %v", err)
		}
		task := resp.GetTask()
		if task == nil {
			t.Fatal("expected task result")
		}
		return task
	}

	// Unset -> full conversation history (m1, m2, request).
	task := send("r1", nil)
	if len(task.History) != 3 {
		t.Errorf("unset historyLength: got %d messages, want 3", len(task.History))
	}

	// N -> the most recent N messages.
	two := 2
	task = send("r2", &two)
	if len(task.History) != 2 || task.History[1].Parts[0].TextContent() != "r2" {
		t.Errorf("historyLength=2: got %+v", task.History)
	}

	// 0 -> no messages.
	zero := 0
	task = send("r3", &zero)
	if len(task.History) != 0 {
		t.Errorf("historyLength=0: got %d messages, want 0", len(task.History))
	}
}

func TestOnSendMessageCloseInWorkingFails(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("do it", "ctx-fail"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		t.Fatal("expected task result")
	}
	if task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("expected FAILED after close-in-working, got %s", task.Status.State)
	}
	if task.Status.Message == nil ||
		!strings.Contains(task.Status.Message.Parts[0].TextContent(), "processor finished without terminal state") {
		t.Errorf("expected failure reason in status message, got %+v", task.Status.Message)
	}

	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateFailed {
		t.Errorf("stored state = %s, want FAILED", stored.Status.State)
	}
}

func TestOnSendMessageNoEvents(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())

	_, err := m.OnSendMessage(context.Background(), sendParams("hello", "ctx-empty"))
	if err == nil {
		t.Fatal("expected error for empty execution")
	}
	assertTaskManagerCode(t, err, taskmanager.ErrCodeInternalError)
}

func TestOnSendMessageExecuteError(t *testing.T) {
	wantErr := errors.New("boom")
	m, _ := setupTest(t, executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return nil, wantErr
	}))

	_, err := m.OnSendMessage(context.Background(), sendParams("hello", "ctx-err"))
	if !errors.Is(err, wantErr) {
		t.Errorf("expected processor error passthrough, got %v", err)
	}
}

func TestOnSendMessageNilChannel(t *testing.T) {
	m, _ := setupTest(t, executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return nil, nil
	}))

	_, err := m.OnSendMessage(context.Background(), sendParams("hello", "ctx-nil"))
	assertTaskManagerCode(t, err, taskmanager.ErrCodeInternalError)
}

// =============================================================================
// Continuation (multi-turn) semantics
// =============================================================================

func TestInputRequiredContinuation(t *testing.T) {
	var round int
	var round2EC *taskmanager.ExecContext
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		round++
		out := make(chan protocol.StreamEvent, 2)
		if round == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
		} else {
			round2EC = ec
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("all done"))
		}
		close(out)
		return out, nil
	})
	m, _ := setupTest(t, processor)

	resp1, err := m.OnSendMessage(context.Background(), sendParams("start", "ctx-cont"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	task1 := resp1.GetTask()
	if task1 == nil || task1.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("expected input-required task, got %+v", resp1.Result)
	}
	if task1.Status.Message == nil {
		t.Fatal("expected the input-required question on task.Status.Message")
	}
	storedBeforeFollowUp, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task1.ID})
	if err != nil {
		t.Fatalf("OnGetTask before continuation failed: %v", err)
	}
	for _, snapshot := range []*protocol.Task{task1, storedBeforeFollowUp} {
		if len(snapshot.History) != 1 {
			t.Fatalf("current input-required snapshot must contain only the first user turn in history, got %d", len(snapshot.History))
		}
		for _, message := range snapshot.History {
			if message.MessageID == snapshot.Status.Message.MessageID {
				t.Fatalf("current status message %s must not also appear in history", message.MessageID)
			}
		}
	}

	// Follow-up on the suspended task: the framework passes the current
	// snapshot via ec.Task.
	params2 := sendParams("more data", "ctx-cont")
	params2.Message.TaskID = &task1.ID
	resp2, err := m.OnSendMessage(context.Background(), params2)
	if err != nil {
		t.Fatalf("round 2 failed: %v", err)
	}
	task2 := resp2.GetTask()
	if task2 == nil || task2.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("expected completed task, got %+v", resp2.Result)
	}
	if task2.ID != task1.ID {
		t.Errorf("continuation changed task ID: %s -> %s", task1.ID, task2.ID)
	}

	if round2EC == nil {
		t.Fatal("processor was not invoked for round 2")
	}
	if round2EC.Task == nil {
		t.Fatal("ec.Task must be non-nil on a continuation")
	}
	if round2EC.Task.Status.State != protocol.TaskStateInputRequired {
		t.Errorf("ec.Task state = %s, want input-required", round2EC.Task.Status.State)
	}
	if round2EC.Task.Status.Message != nil {
		t.Error("continuation must move the previous status message into history and clear it from the current status")
	}
	if round2EC.TaskID != task1.ID {
		t.Errorf("ec.TaskID = %s, want %s", round2EC.TaskID, task1.ID)
	}
	// The input-required question moves into history on follow-up, so the
	// continuation sees it between the two user messages (it was lost before).
	if len(round2EC.History) != 3 {
		t.Fatalf("expected 3 history messages (two user + the agent question), got %d", len(round2EC.History))
	}
	if round2EC.History[1].Role != protocol.MessageRoleAgent {
		t.Errorf("expected the agent input-required question at history[1], got role %s", round2EC.History[1].Role)
	}
}

// A continuation whose first event is a direct Message answers with that
// Message and discards later task events; the suspended task stays untouched.
func TestContinuationDirectMessageDiscardsLaterTaskEvent(t *testing.T) {
	var round int
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		round++
		out := make(chan protocol.StreamEvent, 2)
		if round == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
		} else {
			out <- agentReply("just a clarification")
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}
		close(out)
		return out, nil
	})
	m, _ := setupTest(t, processor)

	resp1, err := m.OnSendMessage(context.Background(), sendParams("start", "ctx-msgonly"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	taskID := resp1.GetTask().ID

	params2 := sendParams("more data", "ctx-msgonly")
	params2.Message.TaskID = &taskID
	resp2, err := m.OnSendMessage(context.Background(), params2)
	if err != nil {
		t.Fatalf("round 2 failed: %v", err)
	}
	if resp2.GetMessage() == nil {
		t.Fatalf("expected a Message result for a message-only continuation, got %+v", resp2.Result)
	}
	// The suspended task must stay untouched.
	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateInputRequired {
		t.Errorf("expected the task to stay input-required, got %s", stored.Status.State)
	}
}

func TestContinuationInheritsTaskContext(t *testing.T) {
	var round int
	var round2EC *taskmanager.ExecContext
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		round++
		out := make(chan protocol.StreamEvent, 1)
		if round == 1 {
			out <- statusEvent(protocol.TaskStateInputRequired, nil)
		} else {
			round2EC = ec
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}
		close(out)
		return out, nil
	})
	m, _ := setupTest(t, processor)

	resp1, err := m.OnSendMessage(context.Background(), sendParams("start", "ctx-inherit"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	task1 := resp1.GetTask()

	// The follow-up carries no contextId: it continues the task's conversation.
	params2 := sendParams("more", "")
	params2.Message.TaskID = &task1.ID
	if _, err := m.OnSendMessage(context.Background(), params2); err != nil {
		t.Fatalf("round 2 failed: %v", err)
	}
	if round2EC == nil {
		t.Fatal("processor was not invoked for round 2")
	}
	if round2EC.ContextID != "ctx-inherit" {
		t.Errorf("ec.ContextID = %q, want inherited %q", round2EC.ContextID, "ctx-inherit")
	}
	if len(round2EC.History) != 2 {
		t.Errorf("expected both turns in inherited history, got %d", len(round2EC.History))
	}
}

func TestTerminalTaskSendRejected(t *testing.T) {
	invoked := false
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		invoked = true
		out := make(chan protocol.StreamEvent)
		close(out)
		return out, nil
	})
	m, mr := setupTest(t, processor)
	storedTask(t, m, "task-frozen", "ctx-frozen", protocol.TaskStateCompleted)

	params := sendParams("again", "ctx-frozen")
	taskID := "task-frozen"
	params.Message.TaskID = &taskID
	_, err := m.OnSendMessage(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for terminal task")
	}
	taskErr := decodeTaskManagerError(t, err)
	assertTaskManagerCode(t, err, taskmanager.ErrCodeInvalidParams)
	if data, _ := taskErr.Data.(string); !strings.Contains(data, "task task-frozen is in terminal state") {
		t.Errorf("expected terminal-state reason in error data, got %v", taskErr.Data)
	}
	if invoked {
		t.Error("processor must not be invoked for a terminal task")
	}
	// The rejection happens before the request message is stored.
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, tenantKeyPrefix("")+messagePrefix) ||
			strings.HasPrefix(key, tenantKeyPrefix("")+conversationPrefix) {
			t.Errorf("rejected send must not store the request message, found %s", key)
		}
	}
}

func TestContinuationUnknownTask(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())

	params := sendParams("hello", "ctx-miss")
	taskID := "task-missing"
	params.Message.TaskID = &taskID
	_, err := m.OnSendMessage(context.Background(), params)
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("expected task-not-found, got %v", err)
	}
}

// =============================================================================
// Contract violations
// =============================================================================

func TestForeignTaskEventFails(t *testing.T) {
	foreign := statusEvent(protocol.TaskStateWorking, nil)
	foreign.TaskID = "task-foreign"
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		foreign,
		// Emitted after the violation: must be drained and discarded.
		statusEvent(protocol.TaskStateCompleted, nil),
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("go", "ctx-foreign"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		t.Fatal("expected task result")
	}
	if task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("expected FAILED after foreign-task event, got %s", task.Status.State)
	}
	if task.Status.Message == nil ||
		!strings.Contains(task.Status.Message.Parts[0].TextContent(), "foreign task") {
		t.Errorf("expected foreign-task reason, got %+v", task.Status.Message)
	}

	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateFailed {
		t.Errorf("stored state = %s, want FAILED (completed event must be discarded)", stored.Status.State)
	}
}

func TestTaskSnapshotEventFails(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		&protocol.Task{ID: "task-whatever"},
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("go", "ctx-snapshot"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("expected FAILED after forbidden Task snapshot, got %+v", resp.Result)
	}
}

func TestTaskSnapshotEventWithoutTaskLeavesNoTrace(t *testing.T) {
	m, mr := setupTest(t, scriptedExecutor(&protocol.Task{ID: "task-x"}))

	_, err := m.OnSendMessage(context.Background(), sendParams("go", "ctx-trace"))
	assertTaskManagerCode(t, err, taskmanager.ErrCodeInternalError)
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, tenantKeyPrefix("")+taskPrefix) {
			t.Errorf("violation before task creation must not persist a task, found %s", key)
		}
	}
}

// =============================================================================
// message/stream
// =============================================================================

func TestOnSendMessageStreamOrderAndPersistence(t *testing.T) {
	step := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-step
			out <- artifactEvent("artifact-1", "chunk")
			<-step
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	one := 1
	params := sendParams("stream it", "ctx-stream")
	params.Configuration = &protocol.SendMessageConfiguration{HistoryLength: &one}
	ch, err := m.OnSendMessageStream(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}

	// Frame 1: the protocol-level initial Task snapshot. The first processor
	// update is already persisted before either task frame is delivered.
	frame1 := <-ch
	initial := frame1.GetTask()
	if initial == nil || initial.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("frame 1: expected submitted Task, got %+v", frame1.Result)
	}
	storedInitial, err := m.getTaskInternal(context.Background(), "", "", initial.ID)
	if err != nil {
		t.Fatalf("getTaskInternal failed: %v", err)
	}
	if len(storedInitial.History) != 0 {
		t.Fatalf("initial Task history must not be written into Redis, got %d entries", len(storedInitial.History))
	}
	if len(initial.History) != 1 || textOfMessage(initial.History[0]) != "stream it" {
		t.Fatalf("frame 1: historyLength=1 was not applied: %+v", initial.History)
	}

	// Frame 2: working. Persist-before-broadcast means the store must already
	// reflect the state when the frame is delivered.
	frame2 := <-ch
	statusUpdate := frame2.GetStatusUpdate()
	if statusUpdate == nil || statusUpdate.Status.State != protocol.TaskStateWorking {
		t.Fatalf("frame 2: expected working status, got %+v", frame2.Result)
	}
	taskID := statusUpdate.TaskID
	if taskID == "" || statusUpdate.ContextID != "ctx-stream" {
		t.Fatalf("frame 2: expected stamped IDs, got %+v", statusUpdate)
	}
	if initial.ID != taskID || initial.ContextID != statusUpdate.ContextID {
		t.Fatalf("initial Task IDs = %s/%s, want %s/%s",
			initial.ID, initial.ContextID, taskID, statusUpdate.ContextID)
	}
	stored, err := m.getTaskInternal(context.Background(), "", "", taskID)
	if err != nil {
		t.Fatalf("task not persisted before broadcast: %v", err)
	}
	if stored.Status.State != protocol.TaskStateWorking {
		t.Errorf("stored state = %s, want working", stored.Status.State)
	}
	step <- struct{}{}

	// Frame 3: artifact, persisted before delivery.
	frame3 := <-ch
	artifactUpdate := frame3.GetArtifactUpdate()
	if artifactUpdate == nil || artifactUpdate.Artifact.ArtifactID != "artifact-1" {
		t.Fatalf("frame 3: expected artifact update, got %+v", frame3.Result)
	}
	stored, err = m.getTaskInternal(context.Background(), "", "", taskID)
	if err != nil {
		t.Fatalf("getTaskInternal failed: %v", err)
	}
	if len(stored.Artifacts) != 1 || stored.Artifacts[0].ArtifactID != "artifact-1" {
		t.Errorf("artifact not persisted before broadcast: %+v", stored.Artifacts)
	}
	step <- struct{}{}

	// Frame 4: completed, then the pipe closes.
	frame4 := <-ch
	statusUpdate = frame4.GetStatusUpdate()
	if statusUpdate == nil || statusUpdate.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("frame 4: expected completed status, got %+v", frame4.Result)
	}
	stored, err = m.getTaskInternal(context.Background(), "", "", taskID)
	if err != nil {
		t.Fatalf("getTaskInternal failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCompleted {
		t.Errorf("stored state = %s, want completed", stored.Status.State)
	}
	if _, ok := <-ch; ok {
		t.Error("expected stream closed after engine end")
	}
}

func TestOnSendMessageStreamArtifactFirstStartsWithTask(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		artifactEvent("artifact-first", "chunk"),
		statusEvent(protocol.TaskStateCompleted, nil),
	))

	stream, err := m.OnSendMessageStream(context.Background(), sendParams("stream it", "ctx-artifact-first"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	frames := collectStream(t, stream)
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want Task, artifact, completed status: %+v", len(frames), frames)
	}
	initial := frames[0].GetTask()
	if initial == nil || initial.Status.State != protocol.TaskStateSubmitted ||
		initial.ContextID != "ctx-artifact-first" {
		t.Fatalf("first frame = %+v, want submitted Task", frames[0].Result)
	}
	artifact := frames[1].GetArtifactUpdate()
	if artifact == nil || artifact.TaskID != initial.ID || artifact.Artifact.ArtifactID != "artifact-first" {
		t.Fatalf("second frame = %+v, want artifact for %s", frames[1].Result, initial.ID)
	}
	completed := frames[2].GetStatusUpdate()
	if completed == nil || completed.TaskID != initial.ID || completed.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("third frame = %+v, want completed status for %s", frames[2].Result, initial.ID)
	}
}

func TestOnSendMessageStreamContinuationStartsWithCurrentTask(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(statusEvent(protocol.TaskStateCompleted, nil)))
	task := storedTask(t, m, "task-continuation-stream", "ctx-continuation-stream", protocol.TaskStateInputRequired)
	task.Artifacts = []protocol.Artifact{{
		ArtifactID: "existing-artifact",
		Parts:      []*protocol.Part{protocol.NewTextPart("existing")},
	}}
	if err := m.storeTask(context.Background(), "", "", task); err != nil {
		t.Fatalf("storeTask with artifact failed: %v", err)
	}

	params := sendParams("continue", "")
	params.Message.TaskID = &task.ID
	stream, err := m.OnSendMessageStream(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessageStream continuation failed: %v", err)
	}
	frames := collectStream(t, stream)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want current Task then completed status: %+v", len(frames), frames)
	}
	initial := frames[0].GetTask()
	if initial == nil || initial.ID != task.ID || initial.ContextID != task.ContextID ||
		initial.Status.State != protocol.TaskStateInputRequired || len(initial.Artifacts) != 1 ||
		initial.Artifacts[0].ArtifactID != "existing-artifact" {
		t.Fatalf("first frame = %+v, want pre-update continuation Task", frames[0].Result)
	}
	completed := frames[1].GetStatusUpdate()
	if completed == nil || completed.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("second frame = %+v, want completed status", frames[1].Result)
	}
}

func TestOnSendMessageStreamPureMessage(t *testing.T) {
	m, mr := setupTest(t, scriptedExecutor(agentReply("streamed reply")))

	ch, err := m.OnSendMessageStream(context.Background(), sendParams("hello", "ctx-stream-msg"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	frame := <-ch
	msg := frame.GetMessage()
	if msg == nil || msg.Parts[0].TextContent() != "streamed reply" {
		t.Fatalf("expected message frame, got %+v", frame.Result)
	}
	if _, ok := <-ch; ok {
		t.Error("expected stream closed")
	}
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, tenantKeyPrefix("")+taskPrefix) {
			t.Errorf("pure-message stream must not create a task, found %s", key)
		}
	}
}

// The first valid event fixes the response shape. Once a direct Message has
// been sent, later task events are drained rather than switching wire modes.
func TestOnSendMessageStreamDirectMessageDoesNotSwitchToTask(t *testing.T) {
	m, mr := setupTest(t, scriptedExecutor(
		agentReply("direct"),
		agentReply("ignored"),
		statusEvent(protocol.TaskStateCompleted, nil),
	))

	ch, err := m.OnSendMessageStream(context.Background(), sendParams("hello", "ctx-direct-mode"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	frames := collectStream(t, ch)
	if len(frames) != 1 || frames[0].GetMessage() == nil ||
		textOfMessage(*frames[0].GetMessage()) != "direct" {
		t.Fatalf("expected exactly the first direct Message, got %+v", frames)
	}
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, tenantKeyPrefix("")+taskPrefix) {
			t.Fatalf("task event after direct Message must be discarded, found %s", key)
		}
	}
}

func TestOnSendMessageStreamBlockingSend(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		artifactEvent("artifact-1", "a"),
		statusEvent(protocol.TaskStateCompleted, nil),
	), WithTaskSubscriberBufferSize(1), WithTaskSubscriberBlockingSend(true))

	ch, err := m.OnSendMessageStream(context.Background(), sendParams("go", "ctx-blocking"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var states []string
	for frame := range ch {
		switch {
		case frame.GetStatusUpdate() != nil:
			states = append(states, string(frame.GetStatusUpdate().Status.State))
		case frame.GetArtifactUpdate() != nil:
			states = append(states, "artifact")
		}
	}
	want := []string{
		string(protocol.TaskStateWorking),
		"artifact",
		string(protocol.TaskStateCompleted),
	}
	if len(states) != len(want) {
		t.Fatalf("expected %d frames, got %v", len(want), states)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Errorf("frame %d = %s, want %s (order must match emission)", i, states[i], want[i])
		}
	}
}

func TestOnSendMessageStreamNonBlockingBufferReservesInitialTaskSlot(t *testing.T) {
	taskID := make(chan string, 1)
	processor := executorFunc(func(
		_ context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		taskID <- ec.TaskID
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateCompleted, nil)
		close(out)
		return out, nil
	})
	m, _ := setupTest(t, processor,
		WithTaskSubscriberBufferSize(1),
		WithTaskSubscriberBlockingSend(false),
	)

	stream, err := m.OnSendMessageStream(context.Background(), sendParams("go", "ctx-nonblocking-buffer"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	id := <-taskID
	// Do not consume the response until the terminal event has been committed
	// and the pipe closed. This makes the capacity requirement deterministic.
	waitTaskState(t, m, id, protocol.TaskStateCompleted)
	frames := collectStream(t, stream)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want initial Task and completed status: %+v", len(frames), frames)
	}
	initial := frames[0].GetTask()
	if initial == nil || initial.ID != id || initial.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("first frame = %+v, want submitted Task for %s", frames[0].Result, id)
	}
	completed := frames[1].GetStatusUpdate()
	if completed == nil || completed.TaskID != id || completed.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("second frame = %+v, want completed status for %s", frames[1].Result, id)
	}
}

// =============================================================================
// returnImmediately
// =============================================================================

func TestOnSendMessageReturnImmediately(t *testing.T) {
	release := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-release
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	returnImmediately := true
	params := sendParams("long job", "ctx-ri")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	resp, err := m.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		t.Fatalf("expected first task snapshot, got %+v", resp.Result)
	}
	if task.Status.State != protocol.TaskStateWorking {
		t.Errorf("first snapshot state = %s, want working", task.Status.State)
	}

	// Execution continues in the background; observe completion via resubscribe.
	ch, err := m.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe failed: %v", err)
	}
	close(release)
	var last protocol.TaskState
	for frame := range ch {
		if su := frame.GetStatusUpdate(); su != nil {
			last = su.Status.State
		}
	}
	if last != protocol.TaskStateCompleted {
		t.Errorf("expected completed via subscription, got %s", last)
	}
	waitTaskState(t, m, task.ID, protocol.TaskStateCompleted)
}

func TestOnSendMessageReturnImmediatelyPureMessage(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(agentReply("quick reply")))

	returnImmediately := true
	params := sendParams("hi", "ctx-ri-msg")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	resp, err := m.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	if resp.GetMessage() == nil || resp.GetMessage().Parts[0].TextContent() != "quick reply" {
		t.Fatalf("expected first message as immediateResult result, got %+v", resp.Result)
	}
}

// =============================================================================
// Cancellation
// =============================================================================

func TestOnCancelTaskLiveExecution(t *testing.T) {
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			// Wind down on cancellation without emitting a terminal state:
			// the framework marks the task CANCELED.
			<-ctx.Done()
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	ch, err := m.OnSendMessageStream(context.Background(), sendParams("job", "ctx-cancel"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	frame := <-ch
	task := frame.GetTask()
	if task == nil {
		t.Fatalf("first stream frame = %+v, want Task", frame.Result)
	}
	taskID := task.ID
	frame = <-ch
	if su := frame.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateWorking {
		t.Fatalf("expected WORKING after initial Task, got %+v", frame.Result)
	}

	snapshot, err := m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}
	if snapshot.Status.State != protocol.TaskStateWorking {
		t.Errorf("live-cancel snapshot state = %s, want current WORKING", snapshot.Status.State)
	}

	frame = <-ch
	if su := frame.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("expected CANCELED frame, got %+v", frame.Result)
	}
	if _, ok := <-ch; ok {
		t.Error("expected stream closed after cancellation")
	}

	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCanceled {
		t.Errorf("stored state = %s, want CANCELED", stored.Status.State)
	}
}

func TestOnCancelTaskLiveUnaryExecution(t *testing.T) {
	taskID := make(chan string, 1)
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		taskID <- ec.TaskID
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-ctx.Done()
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor, WithExpireTime(600*time.Millisecond))
	t.Cleanup(func() { _ = m.Close() })

	type sendResult struct {
		response *protocol.SendMessageResponse
		err      error
	}
	sendDone := make(chan sendResult, 1)
	go func() {
		response, err := m.OnSendMessage(context.Background(), sendParams("job", "ctx-unary-cancel"))
		sendDone <- sendResult{response: response, err: err}
	}()
	id := <-taskID
	waitTaskState(t, m, id, protocol.TaskStateWorking)

	snapshot, err := m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: id})
	if err != nil {
		t.Fatalf("OnCancelTask: %v", err)
	}
	if snapshot == nil || snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("cancel snapshot = %+v, want WORKING", snapshot)
	}
	select {
	case got := <-sendDone:
		if got.err != nil {
			t.Fatalf("OnSendMessage: %v", got.err)
		}
		canceled := got.response.GetTask()
		if canceled == nil || canceled.Status.State != protocol.TaskStateCanceled {
			t.Fatalf("send response = %+v, want CANCELED Task", got.response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocking unary send did not finish after cancellation")
	}
	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: id})
	if err != nil || stored.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("stored task = %+v, err=%v; want CANCELED", stored, err)
	}
}

// TestOnCancelTaskAllowsLateCompleted verifies a racing terminal result can win cancellation.
func TestOnCancelTaskAllowsLateCompleted(t *testing.T) {
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-ctx.Done()
			// A terminal event that wins the owner close-rule race is retained.
			out <- statusEvent(protocol.TaskStateCompleted, agentReply("finished anyway"))
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	ch, err := m.OnSendMessageStream(context.Background(), sendParams("job", "ctx-race"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	frame := <-ch
	task := frame.GetTask()
	if task == nil {
		t.Fatalf("first stream frame = %+v, want Task", frame.Result)
	}
	taskID := task.ID
	frame = <-ch
	if su := frame.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateWorking {
		t.Fatalf("expected WORKING after initial Task, got %+v", frame.Result)
	}

	if _, err := m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID}); err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}

	frame = <-ch
	if su := frame.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("expected COMPLETED frame, got %+v", frame.Result)
	}
	if _, ok := <-ch; ok {
		t.Error("expected stream closed")
	}

	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCompleted {
		t.Errorf("stored state = %s, want COMPLETED", stored.Status.State)
	}
}

func TestOnCancelTaskWithoutLiveExecution(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	storedTask(t, m, "task-idle", "ctx-idle", protocol.TaskStateWorking)

	// A subscriber must receive the CANCELED broadcast.
	ch, err := m.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: "task-idle"})
	if err != nil {
		t.Fatalf("OnResubscribe failed: %v", err)
	}
	first := <-ch
	if first.GetTask() == nil {
		t.Fatalf("expected Task snapshot as first frame, got %+v", first.Result)
	}

	canceled, err := m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: "task-idle"})
	if err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}
	if canceled.Status.State != protocol.TaskStateCanceled {
		t.Errorf("returned state = %s, want CANCELED", canceled.Status.State)
	}

	frame := <-ch
	if su := frame.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("expected CANCELED frame for subscriber, got %+v", frame.Result)
	}
	if _, ok := <-ch; ok {
		t.Error("expected subscriber closed after terminal broadcast")
	}

	stored, err := m.getTaskInternal(context.Background(), "", "", "task-idle")
	if err != nil {
		t.Fatalf("getTaskInternal failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateCanceled {
		t.Errorf("stored state = %s, want CANCELED", stored.Status.State)
	}
}

func TestOnCancelTaskTerminalAndMissing(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	storedTask(t, m, "task-done", "ctx-done", protocol.TaskStateCompleted)

	if _, err := m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: "task-done"}); !errors.Is(
		err, taskmanager.ErrTaskNotCancelableSentinel) {
		t.Errorf("expected not-cancelable for terminal task, got %v", err)
	}
	if _, err := m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: "task-nope"}); !errors.Is(
		err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("expected task-not-found, got %v", err)
	}
}

// =============================================================================
// Client disconnect semantics
// =============================================================================

func TestOnSendMessageRequestContextDetached(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			close(started)
			<-release
			// The processor context must survive the request context's death.
			if ctx.Err() != nil {
				out <- statusEvent(protocol.TaskStateFailed, agentReply("ctx was canceled"))
				return
			}
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	type result struct {
		resp *protocol.SendMessageResponse
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		params := sendParams("detach", "ctx-detach")
		resp, err := m.OnSendMessage(reqCtx, params)
		resultCh <- result{resp, err}
	}()

	<-started
	cancelReq()
	res := <-resultCh
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("expected ctx.Err() from blocking wait, got %v", res.err)
	}
	close(release)

	// The engine kept running: find the task and wait for completion.
	var taskID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && taskID == "" {
		listed, err := m.OnListTasks(context.Background(), protocol.ListTasksParams{ContextID: "ctx-detach"})
		if err == nil && len(listed.Tasks) == 1 {
			taskID = listed.Tasks[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("task not found after request context death")
	}
	waitTaskState(t, m, taskID, protocol.TaskStateCompleted)
}

// =============================================================================
// Resubscribe
// =============================================================================

func TestOnResubscribeTerminalAndMissing(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	storedTask(t, m, "task-final", "ctx-final", protocol.TaskStateCompleted)

	if _, err := m.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: "task-final"}); !errors.Is(
		err, taskmanager.ErrUnsupportedOperationSentinel) {
		t.Errorf("expected unsupported-operation for terminal task, got %v", err)
	}
	if _, err := m.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: "task-nope"}); !errors.Is(
		err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("expected task-not-found, got %v", err)
	}
}

func TestOnResubscribeReceivesLiveEvents(t *testing.T) {
	release := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-release
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	ch, err := m.OnSendMessageStream(context.Background(), sendParams("job", "ctx-resub"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	frame := <-ch
	task := frame.GetTask()
	if task == nil {
		t.Fatalf("first stream frame = %+v, want Task", frame.Result)
	}
	taskID := task.ID
	frame = <-ch
	if su := frame.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateWorking {
		t.Fatalf("expected WORKING after initial Task, got %+v", frame.Result)
	}

	sub, err := m.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnResubscribe failed: %v", err)
	}
	// v1.0: the first frame is the current Task snapshot.
	first := <-sub
	snapshot := first.GetTask()
	if snapshot == nil || snapshot.ID != taskID || snapshot.Status.State != protocol.TaskStateWorking {
		t.Fatalf("expected working Task snapshot first, got %+v", first.Result)
	}

	close(release)
	second := <-sub
	if su := second.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("expected completed status, got %+v", second.Result)
	}
	if _, ok := <-sub; ok {
		t.Error("expected subscriber closed after terminal event")
	}
	// Drain the request pipe as well.
	for range ch {
	}
}

// =============================================================================
// GetTask history semantics
// =============================================================================

func TestOnGetTaskHistoryLength(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	contextID := "ctx-hist-sem"
	for _, text := range []string{"m1", "m2", "m3"} {
		msg := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart(text)})
		msg.ContextID = &contextID
		m.storeMessage(context.Background(), "", "", msg)
	}
	storedTask(t, m, "task-hist", contextID, protocol.TaskStateWorking)

	// Unset -> full history.
	task, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: "task-hist"})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(task.History) != 3 {
		t.Errorf("unset historyLength: got %d messages, want 3", len(task.History))
	}

	// N -> most recent N.
	two := 2
	task, err = m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: "task-hist", HistoryLength: &two})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(task.History) != 2 || task.History[1].Parts[0].TextContent() != "m3" {
		t.Errorf("historyLength=2: got %+v", task.History)
	}

	// 0 -> no messages.
	zero := 0
	task, err = m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: "task-hist", HistoryLength: &zero})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(task.History) != 0 {
		t.Errorf("historyLength=0: got %d messages, want 0", len(task.History))
	}
}

// =============================================================================
// ListTasks
// =============================================================================

func TestOnListTasks(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	storedTask(t, m, "task-a1", "ctx-a", protocol.TaskStateCompleted)
	storedTask(t, m, "task-a2", "ctx-a", protocol.TaskStateWorking)
	storedTask(t, m, "task-b1", "ctx-b", protocol.TaskStateWorking)

	result, err := m.OnListTasks(context.Background(), protocol.ListTasksParams{ContextID: "ctx-a"})
	if err != nil {
		t.Fatalf("OnListTasks failed: %v", err)
	}
	if result.TotalSize != 2 || len(result.Tasks) != 2 {
		t.Errorf("contextId filter: got %d/%d tasks, want 2/2", len(result.Tasks), result.TotalSize)
	}

	result, err = m.OnListTasks(context.Background(), protocol.ListTasksParams{Status: protocol.TaskStateWorking})
	if err != nil {
		t.Fatalf("OnListTasks failed: %v", err)
	}
	if result.TotalSize != 2 {
		t.Errorf("status filter: got %d tasks, want 2", result.TotalSize)
	}

	// Pagination: pageSize=2 over 3 tasks yields two pages without overlap.
	pageSize := 2
	page1, err := m.OnListTasks(context.Background(), protocol.ListTasksParams{PageSize: &pageSize})
	if err != nil {
		t.Fatalf("OnListTasks failed: %v", err)
	}
	if len(page1.Tasks) != 2 || page1.NextPageToken == "" || page1.TotalSize != 3 {
		t.Fatalf("page 1: got %d tasks, token %q, total %d", len(page1.Tasks), page1.NextPageToken, page1.TotalSize)
	}
	page2, err := m.OnListTasks(context.Background(), protocol.ListTasksParams{
		PageSize: &pageSize, PageToken: page1.NextPageToken,
	})
	if err != nil {
		t.Fatalf("OnListTasks failed: %v", err)
	}
	if len(page2.Tasks) != 1 || page2.NextPageToken != "" {
		t.Fatalf("page 2: got %d tasks, token %q", len(page2.Tasks), page2.NextPageToken)
	}
	seen := map[string]bool{}
	for _, task := range append(page1.Tasks, page2.Tasks...) {
		if seen[task.ID] {
			t.Errorf("task %s returned twice across pages", task.ID)
		}
		seen[task.ID] = true
	}

	intPointer := func(value int) *int { return &value }
	for _, params := range []protocol.ListTasksParams{
		{PageSize: intPointer(0)},
		{PageSize: intPointer(taskmanager.ListTasksMaxPageSize + 1)},
		{HistoryLength: intPointer(-1)},
	} {
		if _, err := m.OnListTasks(context.Background(), params); !errors.Is(err, taskmanager.ErrInvalidParamsSentinel) {
			t.Errorf("OnListTasks(%+v) error = %v, want invalid params", params, err)
		}
	}
}

// =============================================================================
// Push notification config CRUD
// =============================================================================

func TestPushNotificationCRUD(t *testing.T) { //nolint:gocyclo // One lifecycle test keeps CRUD state transitions explicit.
	m, mr := setupTest(t, scriptedExecutor(), WithPushNotifications(push.Config{Sender: &recordingSender{}}))
	storedTask(t, m, "task-push", "ctx-push", protocol.TaskStateWorking)

	config := protocol.TaskPushNotificationConfig{
		TaskID: "task-push",
		URL:    "https://example.com/webhook",
	}
	first, err := m.OnPushNotificationSet(context.Background(), config)
	if err != nil {
		t.Fatalf("OnPushNotificationSet failed: %v", err)
	}
	second, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: config.TaskID,
		URL:    "https://example.com/second",
	})
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatalf("generated IDs must be distinct: first=%q second=%q", first.ID, second.ID)
	}
	if !mr.Exists(pushNotificationKey("", "", "task-push")) {
		t.Error("push config key missing in redis")
	}
	if got := mr.TTL(pushNotificationKey("", "", "task-push")); got != defaultExpiration {
		t.Errorf("push config TTL = %v, want %v", got, defaultExpiration)
	}

	// An explicit ID registers an additional config for the same task.
	extra, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: config.TaskID,
		ID:     "extra",
		URL:    "https://example.com/extra",
	})
	if err != nil {
		t.Fatalf("set additional config failed: %v", err)
	}
	if extra.ID != "extra" {
		t.Errorf("unexpected additional config: %+v", extra)
	}

	list, err := m.OnPushNotificationList(context.Background(),
		protocol.ListTaskPushNotificationConfigsParams{TaskID: config.TaskID})
	if err != nil {
		t.Fatalf("OnPushNotificationList failed: %v", err)
	}
	if len(list.Configs) != 3 {
		t.Fatalf("list: got %d configs, want 3: %+v", len(list.Configs), list.Configs)
	}
	for i := 1; i < len(list.Configs); i++ {
		if list.Configs[i-1].ID > list.Configs[i].ID {
			t.Fatalf("configs are not sorted by ID: %+v", list.Configs)
		}
	}

	gotExtra, err := m.OnPushNotificationGet(context.Background(),
		protocol.GetTaskPushNotificationConfigParams{TaskID: config.TaskID, ID: extra.ID})
	if err != nil || gotExtra.ID != extra.ID {
		t.Fatalf("get by config ID: config=%+v err=%v", gotExtra, err)
	}
	if _, err := m.OnPushNotificationGet(context.Background(),
		protocol.GetTaskPushNotificationConfigParams{TaskID: config.TaskID, ID: "missing"}); !errors.Is(err, taskmanager.ErrPushConfigNotFoundSentinel) {
		t.Errorf("get missing config ID: got %v, want PushConfigNotFound", err)
	}

	if err := m.OnPushNotificationDelete(context.Background(),
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: config.TaskID, ID: extra.ID}); err != nil {
		t.Fatalf("delete one config failed: %v", err)
	}
	list, err = m.OnPushNotificationList(context.Background(),
		protocol.ListTaskPushNotificationConfigsParams{TaskID: config.TaskID})
	if err != nil || len(list.Configs) != 2 {
		t.Fatalf("list after single delete: configs=%+v err=%v", list.Configs, err)
	}

	err = m.OnPushNotificationDelete(context.Background(),
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: config.TaskID})
	assertTaskManagerCode(t, err, taskmanager.ErrCodeInvalidParams)

	// Public CRUD must not create or expose orphan configs for missing tasks.
	_, err = m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: "task-nope", URL: "https://example.com",
	})
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("set for missing task: got %v, want TaskNotFound", err)
	}
	if _, err := m.OnPushNotificationList(context.Background(),
		protocol.ListTaskPushNotificationConfigsParams{TaskID: "task-nope"}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("list for missing task: got %v, want TaskNotFound", err)
	}
	if err := m.OnPushNotificationDelete(context.Background(),
		protocol.DeleteTaskPushNotificationConfigParams{TaskID: "task-nope", ID: "cfg"}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("delete for missing task: got %v, want TaskNotFound", err)
	}
}

func TestTaskWriteRefreshesPushConfigTTL(t *testing.T) {
	const expire = 30 * time.Minute
	m, mr := setupTest(t, scriptedExecutor(), WithExpireTime(expire),
		WithPushNotifications(push.Config{Sender: &recordingSender{}}))
	task := storedTask(t, m, "task-push-ttl", "ctx-push-ttl", protocol.TaskStateWorking)
	if _, err := m.OnPushNotificationSet(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: task.ID, URL: "https://example.com/webhook",
	}); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(10 * time.Minute)
	if got := mr.TTL(pushNotificationKey("", "", task.ID)); got != 20*time.Minute {
		t.Fatalf("push config TTL before refresh = %v, want 20m", got)
	}
	if err := m.storeTask(context.Background(), "", "", task); err != nil {
		t.Fatal(err)
	}
	if got := mr.TTL(pushNotificationKey("", "", task.ID)); got != expire {
		t.Fatalf("push config TTL after task write = %v, want %v", got, expire)
	}
}

// =============================================================================
// TTL behavior
// =============================================================================

func TestStorageTTL(t *testing.T) {
	expire := 30 * time.Minute
	m, mr := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateCompleted, nil),
	), WithExpireTime(expire))

	resp, err := m.OnSendMessage(context.Background(), sendParams("ttl check", "ctx-ttl"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	taskID := resp.GetTask().ID

	if got := mr.TTL(taskKey("", "", taskID)); got != expire {
		t.Errorf("task TTL = %v, want %v", got, expire)
	}
	if got := mr.TTL(conversationKey("", "", "ctx-ttl")); got != expire {
		t.Errorf("conversation TTL = %v, want %v", got, expire)
	}
	var messageKey string
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, tenantKeyPrefix("")+messagePrefix) {
			messageKey = key
			break
		}
	}
	if messageKey == "" {
		t.Fatal("no message key found")
	}
	if got := mr.TTL(messageKey); got != expire {
		t.Errorf("message TTL = %v, want %v", got, expire)
	}
}

// storeMessage is atomically idempotent by MessageID: concurrent stores (e.g.
// from reply and status paths) leave one conversation index entry.
func TestStoreMessage_IdempotentByMessageID(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	ctx := context.Background()
	cid := "ctx-idem"
	msg := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart("x")})
	msg.ContextID = &cid

	const writers = 32
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			m.storeMessage(ctx, "", "", msg)
		}()
	}
	wg.Wait()

	hist, err := m.getConversationHistory(ctx, "", "", cid, 100)
	if err != nil {
		t.Fatalf("getConversationHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Errorf("a message stored twice under the same MessageID must appear once, got %d", len(hist))
	}
}
