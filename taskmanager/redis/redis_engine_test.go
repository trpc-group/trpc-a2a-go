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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// =============================================================================
// Test helpers (regression suite)
// =============================================================================

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

type rpcErrorPayload struct {
	Code int `json:"code"`
	Data any `json:"data"`
}

func decodeRPCError(t *testing.T, err error) rpcErrorPayload {
	t.Helper()
	payload, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("marshal JSON-RPC error: %v", marshalErr)
	}
	var rpcErr rpcErrorPayload
	if unmarshalErr := json.Unmarshal(payload, &rpcErr); unmarshalErr != nil {
		t.Fatalf("unmarshal JSON-RPC error: %v", unmarshalErr)
	}
	return rpcErr
}

// assertRPCCode fails unless err carries the wanted JSON-RPC code.
func assertRPCCode(t *testing.T, err error, code int) {
	t.Helper()
	if rpcErr := decodeRPCError(t, err); rpcErr.Code != code {
		t.Fatalf("expected JSON-RPC error code %d, got %v", code, err)
	}
}

// liveExecutionCount reports how many executions are still registered.
func liveExecutionCount(m *TaskManager) int {
	m.cancelMu.RLock()
	defer m.cancelMu.RUnlock()
	return len(m.executions)
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

// =============================================================================
// FIX-A: subscriber deadlock — a blocking Send is releasable by Close
// =============================================================================

// A blocking Send on a full buffer must be released by a concurrent Close;
// the old design closed the channel under the same lock the Send held and
// deadlocked.
func TestTaskSubscriber_BlockingSendWedgeReleasableByClose(t *testing.T) {
	sub := newTaskSubscriber("wedge", 1, true)

	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{TaskID: "wedge"})
	// Fill the single buffer slot so the next Send blocks.
	if err := sub.Send(event); err != nil {
		t.Fatalf("unexpected error filling the buffer: %v", err)
	}

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- sub.Send(event)
	}()

	// The Send must be blocked (buffer is full, nobody is draining).
	select {
	case err := <-sendErr:
		t.Fatalf("Send returned before the buffer had room: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Close must unblock the wedged Send instead of deadlocking behind it.
	closed := make(chan struct{})
	go func() {
		sub.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked behind a wedged blocking Send")
	}

	select {
	case err := <-sendErr:
		if err == nil {
			t.Fatal("a Send released by Close must report the subscriber closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wedged Send was not released by Close")
	}
}

// =============================================================================
// FIX-C: evicted slow subscribers must have their channel closed
// =============================================================================

// A non-blocking subscriber whose buffer overflows is evicted from the map and
// its channel must be closed, or its consumer's range loop hangs forever.
func TestNotifySubscribers_EvictedSlowSubscriberChannelIsClosed(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	taskID := "task-slow"

	// Register a non-blocking subscriber with a tiny buffer directly.
	sub := newTaskSubscriber(taskID, 1, false)
	m.subMu.Lock()
	m.subscribers[taskID] = []*taskSubscriber{sub}
	m.subMu.Unlock()

	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{TaskID: taskID})
	// First fills the buffer, second overflows and evicts the subscriber.
	m.notifySubscribers(taskID, event)
	m.notifySubscribers(taskID, event)

	// The evicted subscriber's channel must be closed: draining it must end.
	done := make(chan struct{})
	go func() {
		for range sub.Channel() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("evicted subscriber channel was never closed; consumer range loop hangs")
	}
	if !sub.Closed() {
		t.Error("evicted subscriber must be marked closed")
	}

	// It must also be gone from the map.
	m.subMu.RLock()
	_, exists := m.subscribers[taskID]
	m.subMu.RUnlock()
	if exists {
		t.Error("evicted subscriber must be removed from the map")
	}
}

// =============================================================================
// FIX-D: register-or-reject — a concurrent continuation is rejected
// =============================================================================

// A task admits at most one live run: a continuation while a round is live is
// rejected without invoking the processor, and the first round's artifacts
// survive.
func TestOnSendMessage_ConcurrentContinuationRejected(t *testing.T) {
	release := make(chan struct{})
	var invocations atomic.Int32
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		invocations.Add(1)
		out := make(chan protocol.StreamEvent, 3)
		out <- statusEvent(protocol.TaskStateWorking, nil)
		out <- artifactEvent("art-round1", "round one data")
		go func() {
			<-release
			out <- statusEvent(protocol.TaskStateCompleted, nil)
			close(out)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	pipe, err := m.OnSendMessageStream(context.Background(), sendParams("start", "ctx-concurrent"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	first := recvEvent(t, pipe)
	taskID := first.GetStatusUpdate().TaskID

	// Second live run on the same task must be rejected atomically.
	followUp := sendParams("again", "ctx-concurrent")
	followUp.Message.TaskID = &taskID
	_, err = m.OnSendMessage(context.Background(), followUp)
	assertRPCCode(t, err, taskmanager.ErrCodeInvalidParams)
	if got := invocations.Load(); got != 1 {
		t.Fatalf("processor must not run for the rejected round, invocations=%d", got)
	}

	close(release)
	collectStream(t, pipe)
	waitTaskState(t, m, taskID, protocol.TaskStateCompleted)

	// The round-1 artifact must survive the rejected round.
	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if len(stored.Artifacts) != 1 || stored.Artifacts[0].ArtifactID != "art-round1" {
		t.Errorf("expected the round-1 artifact to survive, got %+v", stored.Artifacts)
	}
}

// =============================================================================
// FIX-H: cancel live-but-terminal → NotCancelable
// =============================================================================

// Canceling a task whose stored state is already terminal fails with
// TaskNotCancelable even while the round is still draining.
func TestOnCancelTask_LiveButTerminalNotCancelable(t *testing.T) {
	release := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateCompleted, nil)
		go func() {
			<-release
			close(out)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	pipe, err := m.OnSendMessageStream(context.Background(), sendParams("quick", "ctx-live-terminal"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	first := recvEvent(t, pipe)
	taskID := first.GetStatusUpdate().TaskID
	waitTaskState(t, m, taskID, protocol.TaskStateCompleted)

	// The round is still live (channel not yet closed) but the stored state is
	// terminal: cancel must be rejected.
	_, err = m.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: taskID})
	assertRPCCode(t, err, taskmanager.ErrCodeTaskNotCancelable)
	close(release)
	collectStream(t, pipe)
}

// =============================================================================
// FIX-I: contextId mismatch rejected before the message is stored
// =============================================================================

// A continuation carrying a foreign contextId is rejected before the processor
// runs and before the message is stored.
func TestOnSendMessage_ContinuationContextMismatchRejected(t *testing.T) {
	var invocations atomic.Int32
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		invocations.Add(1)
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateInputRequired, nil)
		close(out)
		return out, nil
	})
	m, mr := setupTest(t, processor)

	first, err := m.OnSendMessage(context.Background(), sendParams("start", "ctx-mismatch"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	taskID := first.GetTask().ID

	foreignContext := "some-other-context"
	followUp := sendParams("more", "")
	followUp.Message.MessageID = "mismatch-msg"
	followUp.Message.TaskID = &taskID
	followUp.Message.ContextID = &foreignContext
	_, err = m.OnSendMessage(context.Background(), followUp)
	assertRPCCode(t, err, taskmanager.ErrCodeInvalidParams)
	if got := invocations.Load(); got != 1 {
		t.Fatalf("processor must not run for the rejected round, invocations=%d", got)
	}
	// The rejected continuation must not store the request message.
	if mr.Exists(messagePrefix + "mismatch-msg") {
		t.Fatal("rejected continuation must not store the request message")
	}
}

// A continuation rejected during inline push-config validation must not clear
// the current status message or append the rejected user turn to Redis.
func TestOnSendMessage_RejectedContinuationLeavesPersistenceUntouched(t *testing.T) {
	var invocations atomic.Int32
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		invocations.Add(1)
		out := make(chan protocol.StreamEvent, 1)
		out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
		close(out)
		return out, nil
	})
	m, _ := setupTest(t, processor)

	first, err := m.OnSendMessage(context.Background(), sendParams("start", "ctx-rejected-continuation"))
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	taskID := first.GetTask().ID
	taskKey := taskPrefix + taskID
	conversationKey := conversationPrefix + "ctx-rejected-continuation"
	beforeTask, err := m.client.Get(context.Background(), taskKey).Bytes()
	if err != nil {
		t.Fatalf("read task before continuation: %v", err)
	}
	beforeHistory, err := m.client.LRange(context.Background(), conversationKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("read history before continuation: %v", err)
	}

	followUp := sendParams("rejected", "ctx-rejected-continuation")
	followUp.Message.MessageID = "rejected-continuation-message"
	followUp.Message.TaskID = &taskID
	followUp.Configuration = &protocol.SendMessageConfiguration{
		PushConfig: &protocol.TaskPushNotificationConfig{URL: "https://example.com/hook"},
	}
	_, err = m.OnSendMessage(context.Background(), followUp)
	assertRPCCode(t, err, taskmanager.ErrPushNotificationNotSupported().Code)

	afterTask, err := m.client.Get(context.Background(), taskKey).Bytes()
	if err != nil {
		t.Fatalf("read task after continuation: %v", err)
	}
	if !bytes.Equal(afterTask, beforeTask) {
		t.Fatal("rejected continuation changed the stored task snapshot")
	}
	afterHistory, err := m.client.LRange(context.Background(), conversationKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("read history after continuation: %v", err)
	}
	if !reflect.DeepEqual(afterHistory, beforeHistory) {
		t.Fatalf("rejected continuation changed history: before=%v after=%v", beforeHistory, afterHistory)
	}
	if exists, err := m.client.Exists(
		context.Background(), messagePrefix+followUp.Message.MessageID,
	).Result(); err != nil || exists != 0 {
		t.Fatalf("rejected continuation stored its message: exists=%d err=%v", exists, err)
	}
	if got := invocations.Load(); got != 1 {
		t.Fatalf("processor must not run for the rejected round, invocations=%d", got)
	}
}

// =============================================================================
// FIX-F: unspecified state → violation
// =============================================================================

// A status event without a state is a violation: with a live task it fails the
// task; as the only event it leaves zero trace and the round is empty.
func TestEngine_UnspecifiedStateIsViolation(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		statusEvent("", nil),
		statusEvent(protocol.TaskStateCompleted, nil), // must be discarded
	))
	resp, err := m.OnSendMessage(context.Background(), sendParams("go", "ctx-unspec"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("expected a FAILED task, got %+v", resp.Result)
	}
	if got := statusMessageText(task); !strings.Contains(got, "status event without a state") {
		t.Errorf("unexpected violation message: %q", got)
	}
	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateFailed {
		t.Errorf("stored state = %s, want FAILED (completed event must be discarded)", stored.Status.State)
	}

	// Zero-trace variant: the stateless event is the only one -> -32603 and no
	// task:* keys.
	m2, mr2 := setupTest(t, scriptedExecutor(statusEvent(protocol.TaskStateUnspecified, nil)))
	_, err = m2.OnSendMessage(context.Background(), sendParams("go", "ctx-unspec2"))
	assertRPCCode(t, err, taskmanager.ErrCodeInternalError)
	for _, key := range mr2.Keys() {
		if strings.HasPrefix(key, taskPrefix) {
			t.Errorf("violation before any task must leave no task key, found %s", key)
		}
	}
}

// =============================================================================
// Parity: a foreign Message event fails the task (violation)
// =============================================================================

// A Message event naming a foreign task is a violation like any other event.
func TestEngine_ForeignMessageEventFailsTask(t *testing.T) {
	foreignTaskID := "someone-elses-task"
	leaked := agentReply("leaked reply")
	leaked.TaskID = &foreignTaskID
	m, _ := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		leaked,
		statusEvent(protocol.TaskStateCompleted, nil), // must be discarded
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("hijack", "ctx-foreign-msg"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("expected a FAILED task, got %+v", resp.Result)
	}
	if got := statusMessageText(task); !strings.Contains(got, "processor emitted event for foreign task") {
		t.Errorf("unexpected violation message: %q", got)
	}
	stored, err := m.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask failed: %v", err)
	}
	if stored.Status.State != protocol.TaskStateFailed {
		t.Errorf("stored state = %s, want FAILED", stored.Status.State)
	}
}

// =============================================================================
// Artifacts: artifact-first lazy creation
// =============================================================================

// An artifact arriving before any status event lazily creates the task in the
// submitted state.
func TestEngine_ArtifactLazilyCreatesTask(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor(
		artifactEvent("art-only", "data"),
		statusEvent(protocol.TaskStateCompleted, nil),
	))

	resp, err := m.OnSendMessage(context.Background(), sendParams("artifact first", "ctx-art-first"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("expected a COMPLETED task, got %+v", resp.Result)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].ArtifactID != "art-only" {
		t.Errorf("expected the artifact to be persisted, got %+v", task.Artifacts)
	}
}

// =============================================================================
// §3.3: stream disconnect is not a cancel; drain continues
// =============================================================================

// A client disconnect (request ctx cancel + abandoned pipe) neither cancels the
// processor nor stops the drain.
func TestOnSendMessageStream_DisconnectKeepsRunning(t *testing.T) {
	// The processor samples its ctx AFTER the disconnect but BEFORE finishing:
	// the engine legitimately cancels the ctx as cleanup once the round ends,
	// so only the in-flight state proves the detach.
	midRunErr := make(chan error, 1)
	disconnected := make(chan struct{})
	processor := executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, 4)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			<-disconnected // the client is gone from here on
			midRunErr <- ctx.Err()
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
	m, _ := setupTest(t, processor)

	reqCtx, cancel := context.WithCancel(context.Background())
	pipe, err := m.OnSendMessageStream(reqCtx, sendParams("long", "ctx-disc"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	first := recvEvent(t, pipe)
	taskID := first.GetStatusUpdate().TaskID
	cancel() // client disconnects; the pipe is abandoned from here on
	close(disconnected)

	waitTaskState(t, m, taskID, protocol.TaskStateCompleted)
	if err := <-midRunErr; err != nil {
		t.Fatalf("client disconnect must not cancel the processor ctx: %v", err)
	}
}

// =============================================================================
// §3.5: stream startup failure leaves no trace and no live execution
// =============================================================================

// Startup failures on the stream shape return the error directly and leave no
// task trace and no live execution.
func TestOnSendMessageStream_StartupFailureLeavesNoTrace(t *testing.T) {
	bootErr := errors.New("boot failure")
	m, mr := setupTest(t, executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return nil, bootErr
	}))
	_, err := m.OnSendMessageStream(context.Background(), sendParams("x", "ctx-boot"))
	if !errors.Is(err, bootErr) {
		t.Fatalf("expected the startup error, got %v", err)
	}
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, taskPrefix) {
			t.Fatalf("startup failure must leave no task, found %s", key)
		}
	}
	if n := liveExecutionCount(m); n != 0 {
		t.Fatalf("startup failure must deregister the execution, %d live", n)
	}

	// Nil-channel variant.
	m2, _ := setupTest(t, executorFunc(func(
		ctx context.Context, ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return nil, nil
	}))
	_, err = m2.OnSendMessageStream(context.Background(), sendParams("x", "ctx-boot2"))
	assertRPCCode(t, err, taskmanager.ErrCodeInternalError)
	if n := liveExecutionCount(m2); n != 0 {
		t.Fatalf("nil channel must deregister the execution, %d live", n)
	}
}

// =============================================================================
// §3.1: returnImmediately with an empty round → -32603
// =============================================================================

// returnImmediately with a round that closes before any immediateResult event takes
// the <-done arm and reports the empty execution.
func TestOnSendMessage_ReturnImmediatelyEmptyRound(t *testing.T) {
	m, _ := setupTest(t, scriptedExecutor())
	returnImmediately := true
	params := sendParams("empty", "ctx-ri-empty")
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	_, err := m.OnSendMessage(context.Background(), params)
	assertRPCCode(t, err, taskmanager.ErrCodeInternalError)
}

// §3.4 regression (parity with memory): a streaming client that fires its
// continuation on receiving the input-required frame — while the first round's
// channel is still open — must be accepted, not rejected "already has an active
// execution". The suspended round is deregistered at suspend time.
func TestOnSendMessageStream_SuspendThenContinueAccepted(t *testing.T) {
	gate := make(chan struct{})
	var round atomic.Int32
	processor := executorFunc(
		func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
			out := make(chan protocol.StreamEvent, 4)
			if round.Add(1) == 1 {
				go func() {
					defer close(out)
					out <- statusEvent(protocol.TaskStateWorking, nil)
					out <- statusEvent(protocol.TaskStateInputRequired, agentReply("need more"))
					<-gate // stay un-closed while the continuation is fired
				}()
				return out, nil
			}
			go func() {
				defer close(out)
				out <- statusEvent(protocol.TaskStateCompleted, agentReply("done"))
			}()
			return out, nil
		})
	manager, _ := setupTest(t, processor)
	defer close(gate)

	pipe, err := manager.OnSendMessageStream(context.Background(), sendParams("start", ""))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	var taskID string
	for taskID == "" {
		event := recvEvent(t, pipe)
		if su := event.GetStatusUpdate(); su != nil && su.Status.State == protocol.TaskStateInputRequired {
			taskID = su.TaskID
		}
	}

	followUp := sendParams("more", "")
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
