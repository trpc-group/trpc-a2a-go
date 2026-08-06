// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// =============================================================================
// Test helpers
// =============================================================================

// funcExecutor adapts a function to the taskmanager.MessageProcessor interface.
type funcExecutor func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error)

// ProcessMessage implements taskmanager.MessageProcessor.
func (f funcExecutor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	return f(ctx, ec)
}

// eventsExecutor returns an MessageProcessor that emits the given events and closes
// the channel.
func eventsExecutor(events ...protocol.StreamEvent) funcExecutor {
	return func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, len(events)+1)
		for _, event := range events {
			out <- event
		}
		close(out)
		return out, nil
	}
}

// echoExecutor returns an MessageProcessor that replies with a single message
// (a pure-message exchange: no task comes into existence).
func echoExecutor() funcExecutor {
	return func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- agentReply("Echo: " + textOfMessage(ec.Message))
		close(out)
		return out, nil
	}
}

// newTestManager builds a manager around the processor and closes it at test end.
func newTestManager(t *testing.T, processor taskmanager.MessageProcessor, opts ...TaskManagerOption) *TaskManager {
	t.Helper()
	manager, err := NewTaskManager(processor, opts...)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	t.Cleanup(func() { manager.Close() })
	return manager
}

// userParams builds a message/send request with a single text message.
func userParams(text string) protocol.SendMessageParams {
	return protocol.SendMessageParams{
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []*protocol.Part{protocol.NewTextPart(text)},
		},
	}
}

// agentReply builds an agent message event with a single text part.
func agentReply(text string) *protocol.Message {
	message := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart(text)})
	return &message
}

// statusUpdate builds a status event as an MessageProcessor would: IDs left empty for
// the framework to stamp.
func statusUpdate(state protocol.TaskState, message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: state, Message: message}}
}

// workingEvent is the common "task started" status event.
func workingEvent() *protocol.TaskStatusUpdateEvent {
	return statusUpdate(protocol.TaskStateWorking, nil)
}

// artifactEvent builds an artifact event with IDs left empty.
func artifactEvent(artifactID, text string) *protocol.TaskArtifactUpdateEvent {
	return &protocol.TaskArtifactUpdateEvent{
		Artifact: protocol.Artifact{
			ArtifactID: artifactID,
			Parts:      []*protocol.Part{protocol.NewTextPart(text)},
		},
	}
}

// textOfMessage extracts the first text content from a message.
func textOfMessage(message protocol.Message) string {
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

// statusMessageText extracts the text of a task's status message.
func statusMessageText(task *protocol.Task) string {
	if task == nil || task.Status.Message == nil {
		return ""
	}
	return textOfMessage(*task.Status.Message)
}

// storedTaskState reads the persisted state of a task directly from the store.
func storedTaskState(m *TaskManager, taskID string) (protocol.TaskState, bool) {
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return "", false
	}
	return task.Status.State, true
}

// waitTaskState polls the store until the task reaches the wanted state.
func waitTaskState(t *testing.T, m *TaskManager, taskID string, want protocol.TaskState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := storedTaskState(m, taskID); ok && got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, ok := storedTaskState(m, taskID)
	t.Fatalf("timed out waiting for task %s to reach %s (exists=%v state=%s)", taskID, want, ok, got)
}

// recvEvent receives one stream event or fails the test on timeout/close.
func recvEvent(t *testing.T, ch <-chan protocol.StreamResponse) protocol.StreamResponse {
	t.Helper()
	select {
	case event, ok := <-ch:
		if !ok {
			t.Fatal("stream closed while waiting for an event")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a stream event")
	}
	return protocol.StreamResponse{}
}

// collectStream drains the stream until it closes and returns all events.
func collectStream(t *testing.T, ch <-chan protocol.StreamResponse) []protocol.StreamResponse {
	t.Helper()
	var events []protocol.StreamResponse
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, event)
		case <-deadline:
			t.Fatal("timed out draining the stream")
		}
	}
}

// seedTask inserts a task snapshot directly into the store.
func seedTask(m *TaskManager, task protocol.Task) {
	if task.Status.Timestamp == "" {
		task.Status.Timestamp = nowTimestamp()
	}
	m.taskMu.Lock()
	m.tasks[task.ID] = &task
	m.taskMu.Unlock()
}

// liveExecutionCount reports how many executions are still registered.
func liveExecutionCount(m *TaskManager) int {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	return len(m.executions)
}

// =============================================================================
// Unary result derivation (§3.1)
// =============================================================================

// A pure-message exchange returns the message and leaves no task behind
// (lazy creation proof).
func TestOnSendMessage_PureMessageLeavesNoTask(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(agentReply("hi there")))

	response, err := manager.OnSendMessage(context.Background(), userParams("hello"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	message := response.GetMessage()
	if message == nil {
		t.Fatalf("Expected a Message result, got %+v", response)
	}
	if message.MessageID == "" {
		t.Error("Expected the reply MessageID to be stamped")
	}
	if message.Role != protocol.MessageRoleAgent {
		t.Errorf("Expected agent role, got %s", message.Role)
	}

	// The reply must be stored in the conversation.
	manager.conversationMu.RLock()
	_, stored := manager.messages[message.MessageID]
	manager.conversationMu.RUnlock()
	if !stored {
		t.Error("Reply message not found in storage")
	}

	// Lazy creation: no task may exist after a pure-message exchange.
	manager.taskMu.RLock()
	taskCount := len(manager.tasks)
	manager.taskMu.RUnlock()
	if taskCount != 0 {
		t.Errorf("Expected no task in store, got %d", taskCount)
	}
}

// A working->completed run returns the final Task snapshot and GetTask agrees.
func TestOnSendMessage_TaskCompleted(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		statusUpdate(protocol.TaskStateCompleted, agentReply("done")),
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("Expected a Task result, got %+v", response)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Errorf("Expected COMPLETED, got %s", task.Status.State)
	}
	if got := statusMessageText(task); got != "done" {
		t.Errorf("Expected status message %q, got %q", "done", got)
	}
	// historyLength unset -> the response carries the full conversation: just the
	// stored user message. The completed status message stays on status.Message
	// and is NOT moved into history because it is never superseded.
	if len(task.History) != 1 {
		t.Errorf("Expected 1 history message in the response task, got %d", len(task.History))
	}

	got, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if got.Status.State != protocol.TaskStateCompleted {
		t.Errorf("GetTask state mismatch: got %s", got.Status.State)
	}

	if n := liveExecutionCount(manager); n != 0 {
		t.Errorf("Expected the execution to be deregistered, got %d live", n)
	}
}

// A status message enters history only after a later status supersedes it. The
// new/current message remains on Task.Status and is not duplicated in history.
func TestEngine_SupersededStatusMessageMovesToHistory(t *testing.T) {
	working := agentReply("still working")
	done := agentReply("done")
	manager := newTestManager(t, eventsExecutor(
		statusUpdate(protocol.TaskStateWorking, working),
		statusUpdate(protocol.TaskStateCompleted, done),
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.Message == nil {
		t.Fatalf("Expected a task with a current status message, got %+v", response)
	}
	if len(task.History) != 2 {
		t.Fatalf("Expected user + superseded status message in history, got %d", len(task.History))
	}
	if task.History[1].MessageID != working.MessageID {
		t.Errorf("Expected the superseded working message in history, got %s", task.History[1].MessageID)
	}
	for _, message := range task.History {
		if message.MessageID == task.Status.Message.MessageID {
			t.Fatalf("Current status message %s must not also appear in history", message.MessageID)
		}
	}
}

// Closing the channel without any event is an MessageProcessor bug -> InternalError.
func TestOnSendMessage_EmptyExecutionIsInternalError(t *testing.T) {
	manager := newTestManager(t, eventsExecutor())

	_, err := manager.OnSendMessage(context.Background(), userParams("noop"))
	if err == nil {
		t.Fatal("Expected an error for an empty execution")
	}
	if !errors.Is(err, taskmanager.ErrInternalErrorSentinel) {
		t.Errorf("Expected InternalError, got %v", err)
	}
}

// A nil event channel (with a nil error) is an MessageProcessor bug -> InternalError.
func TestOnSendMessage_NilChannelIsInternalError(t *testing.T) {
	manager := newTestManager(t, funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			return nil, nil
		}))

	_, err := manager.OnSendMessage(context.Background(), userParams("nil"))
	if !errors.Is(err, taskmanager.ErrInternalErrorSentinel) {
		t.Errorf("Expected InternalError for nil channel, got %v", err)
	}
	if n := liveExecutionCount(manager); n != 0 {
		t.Errorf("Expected no live execution after failed start, got %d", n)
	}
}

// An ProcessMessage error is a failed start: the error propagates unchanged and
// nothing is left behind.
func TestOnSendMessage_ExecuteErrorPropagates(t *testing.T) {
	wantErr := errors.New("processor refused to start")
	manager := newTestManager(t, funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			return nil, wantErr
		}))

	_, err := manager.OnSendMessage(context.Background(), userParams("boom"))
	if !errors.Is(err, wantErr) {
		t.Errorf("Expected the processor error to propagate, got %v", err)
	}
	manager.taskMu.RLock()
	taskCount := len(manager.tasks)
	manager.taskMu.RUnlock()
	if taskCount != 0 {
		t.Errorf("Expected no task after failed start, got %d", taskCount)
	}
	if n := liveExecutionCount(manager); n != 0 {
		t.Errorf("Expected no live execution after failed start, got %d", n)
	}
}

// =============================================================================
// Close rules (§3.5)
// =============================================================================

// Closing the channel with the task still WORKING marks it FAILED.
func TestEngine_CloseInWorkingMarksFailed(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(workingEvent()))

	response, err := manager.OnSendMessage(context.Background(), userParams("half done"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("Expected a Task result, got %+v", response)
	}
	if task.Status.State != protocol.TaskStateFailed {
		t.Errorf("Expected FAILED, got %s", task.Status.State)
	}
	if got := statusMessageText(task); !strings.Contains(got, "processor finished without terminal state") {
		t.Errorf("Unexpected failure message: %q", got)
	}
	if state, _ := storedTaskState(manager, task.ID); state != protocol.TaskStateFailed {
		t.Errorf("Store state mismatch: got %s", state)
	}
}

// Closing in INPUT_REQUIRED suspends the task; a follow-up with the same
// taskId re-invokes the MessageProcessor with ec.Task set and can complete it (§3.4).
func TestEngine_InputRequiredSuspendsAndContinues(t *testing.T) {
	var mu sync.Mutex
	var contexts []*taskmanager.ExecContext
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			mu.Lock()
			contexts = append(contexts, ec)
			round := len(contexts)
			mu.Unlock()

			out := make(chan protocol.StreamEvent, 1)
			if round == 1 {
				out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more input"))
			} else {
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("all done"))
			}
			close(out)
			return out, nil
		})
	manager := newTestManager(t, processor)
	ctx := context.Background()

	firstResponse, err := manager.OnSendMessage(ctx, userParams("start"))
	if err != nil {
		t.Fatalf("First OnSendMessage failed: %v", err)
	}
	firstTask := firstResponse.GetTask()
	if firstTask == nil || firstTask.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("Expected an INPUT_REQUIRED task, got %+v", firstResponse)
	}
	if firstTask.Status.Message == nil {
		t.Fatal("Expected the input-required question on task.Status.Message")
	}
	storedBeforeFollowUp, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: firstTask.ID})
	if err != nil {
		t.Fatalf("OnGetTask before continuation failed: %v", err)
	}
	for _, snapshot := range []*protocol.Task{firstTask, storedBeforeFollowUp} {
		if len(snapshot.History) != 1 {
			t.Fatalf("Current input-required snapshot must contain only the first user turn in history, got %d", len(snapshot.History))
		}
		for _, message := range snapshot.History {
			if message.MessageID == snapshot.Status.Message.MessageID {
				t.Fatalf("Current status message %s must not also appear in history", message.MessageID)
			}
		}
	}
	// The close rule must leave the suspended task as-is.
	if state, _ := storedTaskState(manager, firstTask.ID); state != protocol.TaskStateInputRequired {
		t.Fatalf("Expected the task to stay INPUT_REQUIRED, got %s", state)
	}

	// Follow-up with the same taskId and no contextId: it must continue the
	// task's conversation.
	followUp := userParams("here is more input")
	followUp.Message.TaskID = &firstTask.ID
	secondResponse, err := manager.OnSendMessage(ctx, followUp)
	if err != nil {
		t.Fatalf("Continuation OnSendMessage failed: %v", err)
	}
	secondTask := secondResponse.GetTask()
	if secondTask == nil || secondTask.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("Expected a COMPLETED task, got %+v", secondResponse)
	}
	if secondTask.ID != firstTask.ID {
		t.Errorf("Expected the same task ID %s, got %s", firstTask.ID, secondTask.ID)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(contexts) != 2 {
		t.Fatalf("Expected 2 ProcessMessage calls, got %d", len(contexts))
	}
	if contexts[0].Task != nil {
		t.Error("First round must have a nil ec.Task")
	}
	if contexts[1].Task == nil {
		t.Fatal("Continuation must carry the current task snapshot in ec.Task")
	}
	if contexts[1].Task.Status.State != protocol.TaskStateInputRequired {
		t.Errorf("Continuation snapshot state: got %s", contexts[1].Task.Status.State)
	}
	if contexts[1].Task.Status.Message != nil {
		t.Error("Continuation must move the previous status message into history and clear it from the current status")
	}
	if contexts[1].TaskID != firstTask.ID {
		t.Errorf("Continuation ec.TaskID: expected %s, got %s", firstTask.ID, contexts[1].TaskID)
	}
	if contexts[1].ContextID != contexts[0].ContextID {
		t.Errorf("Continuation must inherit the task's contextID: %s vs %s",
			contexts[1].ContextID, contexts[0].ContextID)
	}
	// The input-required question moves into history on follow-up, so the continuation
	// sees it between the two user turns (it was lost before the fold).
	if len(contexts[1].History) != 3 {
		t.Fatalf("Continuation history: want 3 (two user turns + the agent question), got %d", len(contexts[1].History))
	}
	if contexts[1].History[1].Role != protocol.MessageRoleAgent {
		t.Errorf("Expected the agent question at history[1], got role %s", contexts[1].History[1].Role)
	}
}

// A message to a terminal task is rejected with InvalidParams before the
// MessageProcessor is ever invoked.
func TestOnSendMessage_TerminalTaskRejected(t *testing.T) {
	var invoked atomic.Bool
	manager := newTestManager(t, funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			invoked.Store(true)
			out := make(chan protocol.StreamEvent)
			close(out)
			return out, nil
		}))

	seedTask(manager, protocol.Task{
		ID:        "done-task",
		ContextID: "ctx-done",
		Status:    protocol.TaskStatus{State: protocol.TaskStateCompleted},
	})

	params := userParams("try again")
	taskID := "done-task"
	params.Message.TaskID = &taskID
	_, err := manager.OnSendMessage(context.Background(), params)
	if !errors.Is(err, taskmanager.ErrInvalidParamsSentinel) {
		t.Errorf("Expected InvalidParams for a terminal task, got %v", err)
	}
	if invoked.Load() {
		t.Error("MessageProcessor must not be invoked for a terminal task")
	}

	// An unknown taskId is TaskNotFound, also without invoking the MessageProcessor.
	missing := "missing-task"
	params.Message.TaskID = &missing
	_, err = manager.OnSendMessage(context.Background(), params)
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("Expected TaskNotFound, got %v", err)
	}
	if invoked.Load() {
		t.Error("MessageProcessor must not be invoked for an unknown task")
	}
}

// =============================================================================
// Contract violations (§3.2)
// =============================================================================

// An event carrying a foreign taskId fails the task and stops event application.
func TestEngine_ForeignTaskEventFailsTask(t *testing.T) {
	foreign := &protocol.TaskStatusUpdateEvent{
		TaskID: "someone-elses-task",
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
	}
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		foreign,
		statusUpdate(protocol.TaskStateCompleted, nil), // must be discarded
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("hijack"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("Expected a FAILED task, got %+v", response)
	}
	if got := statusMessageText(task); !strings.Contains(got, "processor emitted event for foreign task") {
		t.Errorf("Unexpected violation message: %q", got)
	}
	// The trailing COMPLETED event must not have been applied.
	if state, _ := storedTaskState(manager, task.ID); state != protocol.TaskStateFailed {
		t.Errorf("Expected the store to stay FAILED, got %s", state)
	}
}

// Emitting a *protocol.Task snapshot is forbidden and fails the task.
func TestEngine_TaskSnapshotEventFailsTask(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		&protocol.Task{ID: "snapshot"},
		statusUpdate(protocol.TaskStateCompleted, nil), // must be discarded
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("snapshot"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("Expected a FAILED task, got %+v", response)
	}
	if got := statusMessageText(task); !strings.Contains(got, "processor emitted forbidden Task snapshot") {
		t.Errorf("Unexpected violation message: %q", got)
	}
}

// A Message event naming a foreign task is a violation like any other event.
func TestEngine_ForeignMessageEventFailsTask(t *testing.T) {
	foreignTaskID := "someone-elses-task"
	leaked := agentReply("leaked reply")
	leaked.TaskID = &foreignTaskID
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		leaked,
		statusUpdate(protocol.TaskStateCompleted, nil), // must be discarded
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("hijack"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("Expected a FAILED task, got %+v", response)
	}
	if got := statusMessageText(task); !strings.Contains(got, "processor emitted event for foreign task") {
		t.Errorf("Unexpected violation message: %q", got)
	}
}

// §3.1: a continuation round that only emits Messages answers with the last
// Message; the suspended task stays untouched.
func TestEngine_ContinuationMessageOnlyReturnsMessage(t *testing.T) {
	var round atomic.Int32
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 1)
			if round.Add(1) == 1 {
				out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more input"))
			} else {
				out <- agentReply("just a clarification")
			}
			close(out)
			return out, nil
		})
	manager := newTestManager(t, processor)
	ctx := context.Background()

	first, err := manager.OnSendMessage(ctx, userParams("start"))
	if err != nil {
		t.Fatalf("First OnSendMessage failed: %v", err)
	}
	taskID := first.GetTask().ID

	followUp := userParams("more")
	followUp.Message.TaskID = &taskID
	second, err := manager.OnSendMessage(ctx, followUp)
	if err != nil {
		t.Fatalf("Continuation OnSendMessage failed: %v", err)
	}
	msg := second.GetMessage()
	if msg == nil {
		t.Fatalf("Expected a Message result for a message-only continuation, got %+v", second)
	}
	if got := textOfMessage(*msg); got != "just a clarification" {
		t.Errorf("Unexpected reply text: %q", got)
	}
	// The suspended task must stay untouched.
	if state, _ := storedTaskState(manager, taskID); state != protocol.TaskStateInputRequired {
		t.Errorf("Expected the task to stay INPUT_REQUIRED, got %s", state)
	}
}

// =============================================================================
// Streaming (§3.6)
// =============================================================================

// Stream events arrive in order with IDs stamped, and each one is persisted
// before it is broadcast: GetTask never lags the stream.
func TestOnSendMessageStream_OrderAndPersistBeforeBroadcast(t *testing.T) {
	gate := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- workingEvent()
				<-gate
				out <- artifactEvent("art-1", "chunk one")
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	ch, err := manager.OnSendMessageStream(context.Background(), userParams("stream"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}

	first := recvEvent(t, ch)
	working := first.GetStatusUpdate()
	if working == nil || working.Status.State != protocol.TaskStateWorking {
		t.Fatalf("Expected the WORKING status first, got %+v", first)
	}
	if working.TaskID == "" || working.ContextID == "" {
		t.Errorf("Expected stamped IDs, got taskID=%q contextID=%q", working.TaskID, working.ContextID)
	}
	// The producer is gated, so the store must reflect exactly this event:
	// persisted before broadcast.
	if state, ok := storedTaskState(manager, working.TaskID); !ok || state != protocol.TaskStateWorking {
		t.Errorf("Expected the store at WORKING when the event is received, got exists=%v state=%s", ok, state)
	}
	close(gate)

	second := recvEvent(t, ch)
	artifact := second.GetArtifactUpdate()
	if artifact == nil || artifact.Artifact.ArtifactID != "art-1" {
		t.Fatalf("Expected the artifact event second, got %+v", second)
	}
	manager.taskMu.RLock()
	artifactCount := len(manager.tasks[working.TaskID].Artifacts)
	manager.taskMu.RUnlock()
	if artifactCount != 1 {
		t.Errorf("Expected 1 persisted artifact when the event is received, got %d", artifactCount)
	}

	third := recvEvent(t, ch)
	completed := third.GetStatusUpdate()
	if completed == nil || completed.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("Expected the COMPLETED status third, got %+v", third)
	}
	if state, _ := storedTaskState(manager, working.TaskID); state != protocol.TaskStateCompleted {
		t.Errorf("Expected the store at COMPLETED when the event is received, got %s", state)
	}

	if rest := collectStream(t, ch); len(rest) != 0 {
		t.Errorf("Expected the stream to close after the terminal event, got %d more events", len(rest))
	}
}

// A pure-message execution streams the message and closes; no task is created.
func TestOnSendMessageStream_PureMessage(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(agentReply("hi")))

	ch, err := manager.OnSendMessageStream(context.Background(), userParams("hello"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	events := collectStream(t, ch)
	if len(events) != 1 || events[0].GetMessage() == nil {
		t.Fatalf("Expected exactly one Message event, got %+v", events)
	}
	manager.taskMu.RLock()
	taskCount := len(manager.tasks)
	manager.taskMu.RUnlock()
	if taskCount != 0 {
		t.Errorf("Expected no task after a pure-message stream, got %d", taskCount)
	}
}

// =============================================================================
// Cancellation (§3.3)
// =============================================================================

// Canceling a live execution cancels the MessageProcessor ctx; when the MessageProcessor closes
// without a terminal state, the engine persists CANCELED and the stream ends
// with the synthetic CANCELED status.
func TestOnCancelTask_LiveExecutionCanceled(t *testing.T) {
	started := make(chan string, 1)
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- workingEvent()
				started <- ec.TaskID
				<-ctx.Done() // honor the cancel, close without a terminal state
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	ch, err := manager.OnSendMessageStream(context.Background(), userParams("long run"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var taskID string
	select {
	case taskID = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the processor to start")
	}
	waitTaskState(t, manager, taskID, protocol.TaskStateWorking)

	snapshot, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}
	if snapshot.ID != taskID {
		t.Errorf("Expected the current snapshot of %s, got %s", taskID, snapshot.ID)
	}

	// The stream must end with the synthetic CANCELED status.
	events := collectStream(t, ch)
	var last *protocol.TaskStatusUpdateEvent
	for _, event := range events {
		if su := event.GetStatusUpdate(); su != nil {
			last = su
		}
	}
	if last == nil || last.Status.State != protocol.TaskStateCanceled {
		t.Fatalf("Expected the stream to end with CANCELED, got %+v", last)
	}
	waitTaskState(t, manager, taskID, protocol.TaskStateCanceled)
}

// If the MessageProcessor emits COMPLETED after the cancel request, the MessageProcessor's
// terminal state wins (§3.3): it may genuinely have finished first.
func TestOnCancelTask_ExecutorTerminalWins(t *testing.T) {
	started := make(chan string, 1)
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- workingEvent()
				started <- ec.TaskID
				<-ctx.Done()
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("finished anyway"))
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	ch, err := manager.OnSendMessageStream(context.Background(), userParams("racing"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var taskID string
	select {
	case taskID = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the processor to start")
	}
	waitTaskState(t, manager, taskID, protocol.TaskStateWorking)

	if _, err := manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID}); err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}
	collectStream(t, ch) // drain until the engine finishes
	if state, _ := storedTaskState(manager, taskID); state != protocol.TaskStateCompleted {
		t.Errorf("Expected the processor's COMPLETED to win over the cancel, got %s", state)
	}
}

// Without a live execution the manager persists CANCELED directly; terminal
// tasks are not cancelable and unknown tasks are not found.
func TestOnCancelTask_NoLiveExecution(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	ctx := context.Background()

	seedTask(manager, protocol.Task{
		ID:        "idle-task",
		ContextID: "ctx-idle",
		Status:    protocol.TaskStatus{State: protocol.TaskStateInputRequired},
	})
	got, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{ID: "idle-task"})
	if err != nil {
		t.Fatalf("OnCancelTask failed: %v", err)
	}
	if got.Status.State != protocol.TaskStateCanceled {
		t.Errorf("Expected CANCELED, got %s", got.Status.State)
	}
	if state, _ := storedTaskState(manager, "idle-task"); state != protocol.TaskStateCanceled {
		t.Errorf("Expected the store at CANCELED, got %s", state)
	}

	seedTask(manager, protocol.Task{
		ID:     "final-task",
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
	})
	if _, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{ID: "final-task"}); !errors.Is(
		err, taskmanager.ErrTaskNotCancelableSentinel) {
		t.Errorf("Expected TaskNotCancelable for a terminal task, got %v", err)
	}

	if _, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{ID: "missing"}); !errors.Is(
		err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Errorf("Expected TaskNotFound, got %v", err)
	}
}

// A client disconnect is not a cancel: the unary caller gets ctx.Err() but the
// detached execution keeps running to completion (§3.3).
func TestOnSendMessage_ClientDisconnectKeepsRunning(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- workingEvent()
				started <- ec.TaskID
				select {
				case <-ctx.Done():
					// Would only fire if the request cancellation leaked in.
					out <- statusUpdate(protocol.TaskStateFailed, agentReply("ctx leaked"))
				case <-release:
					out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
				}
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	errCh := make(chan error, 1)
	go func() {
		_, err := manager.OnSendMessage(reqCtx, userParams("detached"))
		errCh <- err
	}()

	var taskID string
	select {
	case taskID = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the processor to start")
	}

	cancelReq()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnSendMessage did not return after the request ctx died")
	}

	// The execution ctx must still be alive: releasing it completes the task.
	close(release)
	waitTaskState(t, manager, taskID, protocol.TaskStateCompleted)
}

// =============================================================================
// returnImmediately (§3.1)
// =============================================================================

// returnImmediately=true returns the first persisted task snapshot while the
// execution continues in the background.
func TestOnSendMessage_ReturnImmediatelyTask(t *testing.T) {
	release := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- workingEvent()
				<-release
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	returnImmediately := true
	params := userParams("background")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}

	response, err := manager.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("Expected the first task snapshot, got %+v", response)
	}
	if task.Status.State != protocol.TaskStateWorking {
		t.Errorf("Expected the WORKING snapshot, got %s", task.Status.State)
	}

	// The execution keeps running after the early return.
	close(release)
	waitTaskState(t, manager, task.ID, protocol.TaskStateCompleted)
}

// returnImmediately=true with a Message as the immediate result returns
// that message.
func TestOnSendMessage_ReturnImmediatelyMessage(t *testing.T) {
	release := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- agentReply("quick reply")
				<-release
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	returnImmediately := true
	params := userParams("quick")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}

	response, err := manager.OnSendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	message := response.GetMessage()
	if message == nil || textOfMessage(*message) != "quick reply" {
		t.Fatalf("Expected the first message, got %+v", response)
	}
	close(release)
}

// =============================================================================
// Artifacts
// =============================================================================

// Artifact events are persisted onto the task (plain append semantics).
func TestEngine_ArtifactsPersisted(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		artifactEvent("art-1", "chunk one"),
		artifactEvent("art-2", "chunk two"),
		statusUpdate(protocol.TaskStateCompleted, nil),
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("build"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("Expected a Task result, got %+v", response)
	}
	if len(task.Artifacts) != 2 {
		t.Fatalf("Expected 2 artifacts, got %d", len(task.Artifacts))
	}
	if task.Artifacts[0].ArtifactID != "art-1" || task.Artifacts[1].ArtifactID != "art-2" {
		t.Errorf("Artifact order mismatch: %s, %s", task.Artifacts[0].ArtifactID, task.Artifacts[1].ArtifactID)
	}
}

// An artifact arriving before any status event lazily creates the task in the
// submitted state.
func TestEngine_ArtifactLazilyCreatesTask(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(
		artifactEvent("art-only", "data"),
		statusUpdate(protocol.TaskStateCompleted, nil),
	))

	response, err := manager.OnSendMessage(context.Background(), userParams("artifact first"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("Expected a COMPLETED task, got %+v", response)
	}
	if len(task.Artifacts) != 1 {
		t.Errorf("Expected the artifact to be persisted, got %d", len(task.Artifacts))
	}
}

// Artifact chunks sharing an ArtifactID reassemble into a single artifact:
// AppendArtifact extends the earlier AddArtifact chunk's parts rather than
// adding a second entry (covers the append flag on the handle and the
// engine-side merge together).
func TestEngine_ArtifactChunksMergeByID(t *testing.T) {
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			h := taskmanager.NewTaskHandle(ctx, ec)
			go func() {
				defer h.Close()
				h.AddArtifact(protocol.Artifact{
					ArtifactID: "doc",
					Parts:      []*protocol.Part{protocol.NewTextPart("Hello ")},
				}, false)
				h.AppendArtifact(protocol.Artifact{
					ArtifactID: "doc",
					Parts:      []*protocol.Part{protocol.NewTextPart("world")},
				}, true)
				h.UpdateTaskState(protocol.TaskStateCompleted, nil)
			}()
			return h.Events(), nil
		})
	manager := newTestManager(t, processor)

	response, err := manager.OnSendMessage(context.Background(), userParams("stream"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("Expected a Task result, got %+v", response)
	}
	if len(task.Artifacts) != 1 {
		t.Fatalf("Expected the two chunks to merge into 1 artifact, got %d", len(task.Artifacts))
	}
	if got := len(task.Artifacts[0].Parts); got != 2 {
		t.Fatalf("Expected 2 concatenated parts, got %d", got)
	}
	if a, b := task.Artifacts[0].Parts[0].TextContent(), task.Artifacts[0].Parts[1].TextContent(); a != "Hello " || b != "world" {
		t.Errorf("Parts out of order or mismatched: %q, %q", a, b)
	}
}

// A message reused as a reply and then as a status message is indexed only once
// when a later status supersedes it — including DIFFERENT objects carrying the
// same MessageID, which pointer dedup alone would miss.
func TestEngine_StatusMessageDedupByID(t *testing.T) {
	reply := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart("hold on")})
	status := reply // value copy: same MessageID, different address
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			h := taskmanager.NewTaskHandle(ctx, ec)
			go func() {
				defer h.Close()
				h.Reply(&reply)
				h.UpdateTaskState(protocol.TaskStateWorking, &status)
				// Supersedes status, causing the same-MessageID copy above to roll.
				h.UpdateTaskState(protocol.TaskStateInputRequired, agentReply("need input"))
			}()
			return h.Events(), nil
		})
	manager := newTestManager(t, processor)

	response, err := manager.OnSendMessage(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("Expected a Task result, got %+v", response)
	}
	agentMsgs := 0
	for _, m := range task.History {
		if m.Role == protocol.MessageRoleAgent {
			agentMsgs++
		}
	}
	if agentMsgs != 1 {
		t.Errorf("the reused agent message (same MessageID) must appear once in history, got %d", agentMsgs)
	}
}

// =============================================================================
// Resubscribe fan-out
// =============================================================================

// A resubscriber attached mid-run receives the snapshot first, then the
// engine's subsequent events, and its channel closes at the terminal state.
func TestOnResubscribe_ReceivesEngineEvents(t *testing.T) {
	started := make(chan string, 1)
	gate := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent)
			go func() {
				defer close(out)
				out <- workingEvent()
				started <- ec.TaskID
				<-gate
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	ch, err := manager.OnSendMessageStream(context.Background(), userParams("watched"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var taskID string
	select {
	case taskID = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the processor to start")
	}
	waitTaskState(t, manager, taskID, protocol.TaskStateWorking)

	sub, err := manager.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnResubscribe failed: %v", err)
	}
	first := recvEvent(t, sub)
	if first.GetTask() == nil || first.GetTask().Status.State != protocol.TaskStateWorking {
		t.Fatalf("Expected the Task snapshot first, got %+v", first)
	}

	close(gate)
	rest := collectStream(t, sub) // closed by cleanSubscribers at the terminal state
	var sawCompleted bool
	for _, event := range rest {
		if su := event.GetStatusUpdate(); su != nil && su.Status.State == protocol.TaskStateCompleted {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Errorf("Expected the resubscriber to receive the COMPLETED event, got %+v", rest)
	}
	collectStream(t, ch) // drain the request pipe too
}

// =============================================================================
// History trimming
// =============================================================================

// OnGetTask fills history per the v1.0 semantics: unset -> full, 0 -> none,
// N -> the most recent N messages.
func TestOnGetTask_HistoryLength(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		statusUpdate(protocol.TaskStateCompleted, nil),
	))
	ctx := context.Background()

	contextID := "ctx-history"
	params := userParams("first turn")
	params.Message.ContextID = &contextID
	response, err := manager.OnSendMessage(ctx, params)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	taskID := response.GetTask().ID

	got, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(got.History) != 1 {
		t.Errorf("Expected the full history (1 message), got %d", len(got.History))
	}

	zero := 0
	got, err = manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: taskID, HistoryLength: &zero})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(got.History) != 0 {
		t.Errorf("Expected no history with historyLength=0, got %d", len(got.History))
	}

	one := 1
	got, err = manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: taskID, HistoryLength: &one})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(got.History) != 1 {
		t.Errorf("Expected 1 history message, got %d", len(got.History))
	}
}

// TaskHandle keeps the former processor's writing style: old-verb bodies must
// behave identically to raw event emission (same engine underneath).
func TestEngine_TaskHandleFacade(t *testing.T) {
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			h := taskmanager.NewTaskHandle(ctx, ec)
			go func() {
				defer h.Close()
				h.UpdateTaskState(protocol.TaskStateWorking, nil)
				h.AddArtifact(protocol.Artifact{
					ArtifactID: "art-1",
					Parts:      []*protocol.Part{protocol.NewTextPart("data")},
				}, true)
				h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
			}()
			return h.Events(), nil
		})
	manager := newTestManager(t, processor)

	response, err := manager.OnSendMessage(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("Expected a COMPLETED task, got %+v", response)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].ArtifactID != "art-1" {
		t.Errorf("Expected the artifact emitted via the facade, got %+v", task.Artifacts)
	}
}

// A fully synchronous TaskHandle body: emits buffered before Events(), no
// goroutine needed — the engine still derives the unary result.
func TestEngine_SynchronousTaskHandle(t *testing.T) {
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			h := taskmanager.NewTaskHandle(ctx, ec)
			defer h.Close()
			h.Reply(protocol.NewAgentText("sync answer"))
			return h.Events(), nil
		})
	manager := newTestManager(t, processor)

	response, err := manager.OnSendMessage(context.Background(), userParams("q"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	msg := response.GetMessage()
	if msg == nil {
		t.Fatalf("Expected a Message result, got %+v", response)
	}
	if got := textOfMessage(*msg); got != "sync answer" {
		t.Errorf("Unexpected reply text: %q", got)
	}
}

// =============================================================================
// Review-fix regressions: single-writer, terminal immutability, immediateResult
// =============================================================================

// assertRPCCode fails unless err is a *taskmanager.Error with the wanted code.
func assertRPCCode(t *testing.T, err error, code taskmanager.ErrorCode) {
	t.Helper()
	var rpcErr *taskmanager.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != code {
		t.Fatalf("expected task-manager error code %s, got %v", code, err)
	}
}

// A task admits at most one live run: a continuation while a round is live is
// rejected without invoking the processor.
func TestOnSendMessage_ConcurrentContinuationRejected(t *testing.T) {
	release := make(chan struct{})
	var invocations atomic.Int32
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			invocations.Add(1)
			out := make(chan protocol.StreamEvent, 3)
			out <- workingEvent()
			out <- artifactEvent("art-1", "data")
			go func() {
				<-release
				out <- statusUpdate(protocol.TaskStateCompleted, nil)
				close(out)
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("start"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	first := recvEvent(t, pipe)
	taskID := first.GetStatusUpdate().TaskID

	followUp := userParams("again")
	followUp.Message.TaskID = &taskID
	_, err = manager.OnSendMessage(context.Background(), followUp)
	assertRPCCode(t, err, taskmanager.ErrCodeInvalidParams)
	if got := invocations.Load(); got != 1 {
		t.Fatalf("processor must not run for the rejected round, invocations=%d", got)
	}

	close(release)
	collectStream(t, pipe)
	waitTaskState(t, manager, taskID, protocol.TaskStateCompleted)

	// The rejected round must not have disturbed round 1's artifact (the
	// clobber the register-or-reject prevents).
	final, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(final.Artifacts) != 1 || final.Artifacts[0].ArtifactID != "art-1" {
		t.Errorf("round 1 artifact must survive the rejected round, got %+v", final.Artifacts)
	}
}

// Canceling a task whose stored state is already terminal fails with
// TaskNotCancelable even while the round is still draining.
func TestOnCancelTask_LiveButTerminalNotCancelable(t *testing.T) {
	release := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 1)
			out <- statusUpdate(protocol.TaskStateCompleted, nil)
			go func() {
				<-release
				close(out)
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("quick"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	first := recvEvent(t, pipe)
	taskID := first.GetStatusUpdate().TaskID
	waitTaskState(t, manager, taskID, protocol.TaskStateCompleted)

	_, err = manager.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	assertRPCCode(t, err, taskmanager.ErrCodeTaskNotCancelable)
	close(release)
}

// §3.1: the framework-written violation FAILED is immediateResult — a
// returnImmediately caller gets it promptly, not when the violating processor
// finally closes its channel.
func TestOnSendMessage_ReturnImmediatelyViolationIsImmediateResult(t *testing.T) {
	release := make(chan struct{})
	var round atomic.Int32
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 2)
			if round.Add(1) == 1 {
				out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more"))
				close(out)
				return out, nil
			}
			foreign := &protocol.TaskStatusUpdateEvent{
				TaskID: "someone-elses-task",
				Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
			}
			out <- foreign
			go func() {
				<-release
				close(out)
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)
	defer close(release)

	first, err := manager.OnSendMessage(context.Background(), userParams("start"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	taskID := first.GetTask().ID

	returnImmediately := true
	followUp := userParams("again")
	followUp.Message.TaskID = &taskID
	followUp.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}

	done := make(chan *protocol.SendMessageResponse, 1)
	go func() {
		response, sendErr := manager.OnSendMessage(context.Background(), followUp)
		if sendErr != nil {
			t.Errorf("continuation failed: %v", sendErr)
			done <- nil
			return
		}
		done <- response
	}()
	select {
	case response := <-done:
		if response == nil {
			return
		}
		task := response.GetTask()
		if task == nil || task.Status.State != protocol.TaskStateFailed {
			t.Fatalf("expected the FAILED violation snapshot, got %+v", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("returnImmediately caller was starved past the violation persist")
	}
}

// A continuation carrying a foreign contextId is rejected before the
// processor runs and before the message is stored.
func TestOnSendMessage_ContinuationContextMismatchRejected(t *testing.T) {
	var invocations atomic.Int32
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			invocations.Add(1)
			out := make(chan protocol.StreamEvent, 1)
			out <- statusUpdate(protocol.TaskStateInputRequired, nil)
			close(out)
			return out, nil
		})
	manager := newTestManager(t, processor)

	first, err := manager.OnSendMessage(context.Background(), userParams("start"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	taskID := first.GetTask().ID

	foreignContext := "some-other-context"
	followUp := userParams("more")
	followUp.Message.MessageID = "mismatch-msg"
	followUp.Message.TaskID = &taskID
	followUp.Message.ContextID = &foreignContext
	_, err = manager.OnSendMessage(context.Background(), followUp)
	assertRPCCode(t, err, taskmanager.ErrCodeInvalidParams)
	if got := invocations.Load(); got != 1 {
		t.Fatalf("processor must not run for the rejected round, invocations=%d", got)
	}
	manager.conversationMu.RLock()
	_, stored := manager.messages["mismatch-msg"]
	manager.conversationMu.RUnlock()
	if stored {
		t.Fatal("rejected continuation must not store the request message")
	}
}

// A status event without a state is a violation: with a live task it fails the
// task; as the only event it leaves zero trace and the round is empty.
func TestEngine_UnspecifiedStateIsViolation(t *testing.T) {
	manager := newTestManager(t, eventsExecutor(
		workingEvent(),
		statusUpdate("", nil),
		statusUpdate(protocol.TaskStateCompleted, nil), // must be discarded
	))
	response, err := manager.OnSendMessage(context.Background(), userParams("go"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("expected a FAILED task, got %+v", response)
	}
	if got := statusMessageText(task); !strings.Contains(got, "status event without a state") {
		t.Errorf("unexpected violation message: %q", got)
	}

	// Zero-trace variant: the stateless event is the only one.
	manager2 := newTestManager(t, eventsExecutor(statusUpdate(protocol.TaskStateUnspecified, nil)))
	_, err = manager2.OnSendMessage(context.Background(), userParams("go"))
	assertRPCCode(t, err, taskmanager.ErrCodeInternalError)
	manager2.taskMu.RLock()
	taskCount := len(manager2.tasks)
	manager2.taskMu.RUnlock()
	if taskCount != 0 {
		t.Fatalf("violation before any task must leave zero trace, found %d tasks", taskCount)
	}
}

// §3.3 for message/stream: a client disconnect (request ctx cancel + abandoned
// pipe) neither cancels the processor nor stops the drain.
func TestOnSendMessageStream_DisconnectKeepsRunning(t *testing.T) {
	// The processor samples its ctx AFTER the disconnect but BEFORE finishing:
	// the engine legitimately cancels the ctx as cleanup once the round ends,
	// so only the in-flight state proves §3.3.
	midRunErr := make(chan error, 1)
	disconnected := make(chan struct{})
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 4)
			go func() {
				defer close(out)
				out <- workingEvent()
				<-disconnected // the client is gone from here on
				midRunErr <- ctx.Err()
				out <- statusUpdate(protocol.TaskStateCompleted, nil)
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)

	reqCtx, cancel := context.WithCancel(context.Background())
	pipe, err := manager.OnSendMessageStream(reqCtx, userParams("long"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	first := recvEvent(t, pipe)
	taskID := first.GetStatusUpdate().TaskID
	cancel() // client disconnects; the pipe is abandoned from here on
	close(disconnected)

	waitTaskState(t, manager, taskID, protocol.TaskStateCompleted)
	if err := <-midRunErr; err != nil {
		t.Fatalf("client disconnect must not cancel the processor ctx: %v", err)
	}
}

// Startup failures on the stream shape return the error directly and leave no
// task trace and no live execution.
func TestOnSendMessageStream_StartupFailureLeavesNoTrace(t *testing.T) {
	bootErr := errors.New("boot failure")
	manager := newTestManager(t, funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			return nil, bootErr
		}))
	_, err := manager.OnSendMessageStream(context.Background(), userParams("x"))
	if !errors.Is(err, bootErr) {
		t.Fatalf("expected the startup error, got %v", err)
	}
	manager.taskMu.RLock()
	taskCount := len(manager.tasks)
	manager.taskMu.RUnlock()
	if taskCount != 0 {
		t.Fatalf("startup failure must leave no task, found %d", taskCount)
	}
	if n := liveExecutionCount(manager); n != 0 {
		t.Fatalf("startup failure must deregister the execution, %d live", n)
	}

	// Nil-channel variant.
	manager2 := newTestManager(t, funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			return nil, nil
		}))
	_, err = manager2.OnSendMessageStream(context.Background(), userParams("x"))
	assertRPCCode(t, err, taskmanager.ErrCodeInternalError)
	if n := liveExecutionCount(manager2); n != 0 {
		t.Fatalf("nil channel must deregister the execution, %d live", n)
	}
}

// returnImmediately with a round that closes before any immediate result takes
// the <-done arm and reports the empty execution.
func TestOnSendMessage_ReturnImmediatelyEmptyRound(t *testing.T) {
	manager := newTestManager(t, eventsExecutor())
	returnImmediately := true
	params := userParams("empty")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	_, err := manager.OnSendMessage(context.Background(), params)
	assertRPCCode(t, err, taskmanager.ErrCodeInternalError)
}

// getConversationHistory mutates LastAccessTime: concurrent reads of one
// context must be race-free (regression for the read-lock write). Pre-seed the
// conversation, then hammer getConversationHistory directly from many
// goroutines so the LastAccessTime write overlap is large enough to trip the
// race detector deterministically if the lock is wrong.
func TestConversationHistoryConcurrentAccess(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	contextID := "shared-context"

	seed := userParams("seed")
	seed.Message.ContextID = &contextID
	if _, err := manager.OnSendMessage(context.Background(), seed); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				manager.getConversationHistory(contextID, 100)
			}
		}()
	}
	wg.Wait()
}

// §3.4 regression: a streaming client that fires its continuation on receiving
// the input-required frame — while the first round's channel is still open —
// must be accepted, not rejected "already has an active execution". The
// suspended round is deregistered at suspend time, not at channel close.
func TestOnSendMessageStream_SuspendThenContinueAccepted(t *testing.T) {
	gate := make(chan struct{})
	var round atomic.Int32
	processor := funcExecutor(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 4)
			if round.Add(1) == 1 {
				go func() {
					defer close(out)
					out <- workingEvent()
					out <- statusUpdate(protocol.TaskStateInputRequired, agentReply("need more"))
					<-gate // stay un-closed while the continuation is fired
				}()
				return out, nil
			}
			go func() {
				defer close(out)
				out <- statusUpdate(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager := newTestManager(t, processor)
	defer close(gate)

	pipe, err := manager.OnSendMessageStream(context.Background(), userParams("start"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	// Read until the input-required frame to learn the task ID; round 1 is now
	// parked on the gate with its channel still open.
	var taskID string
	for taskID == "" {
		event := recvEvent(t, pipe)
		if su := event.GetStatusUpdate(); su != nil && su.Status.State == protocol.TaskStateInputRequired {
			taskID = su.TaskID
		}
	}
	if taskID == "" {
		t.Fatal("never received the input-required frame")
	}

	// Fire the continuation while round 1's channel is still open.
	followUp := userParams("more")
	followUp.Message.TaskID = &taskID
	response, err := manager.OnSendMessage(context.Background(), followUp)
	if err != nil {
		t.Fatalf("continuation on a suspended task must be accepted, got: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("expected the continuation to complete the task, got %+v", response)
	}
}
