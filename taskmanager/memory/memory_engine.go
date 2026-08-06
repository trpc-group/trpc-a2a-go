// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// =============================================================================
// MessageProcessor drain engine
// =============================================================================

// detachedCtx keeps the values of its parent context but drops its
// cancellation and deadline: an execution must survive a client disconnect
// (contract §3.3). go 1.20 has no context.WithoutCancel, so this is the
// minimal substitute.
type detachedCtx struct{ parent context.Context }

// Deadline reports no deadline: execution is not bounded by the request.
func (detachedCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done returns nil: the detached context is never canceled by its parent.
func (detachedCtx) Done() <-chan struct{} { return nil }

// Err always returns nil, matching the nil Done channel.
func (detachedCtx) Err() error { return nil }

// Value delegates to the parent so request-scoped values keep flowing.
func (d detachedCtx) Value(key any) any { return d.parent.Value(key) }

// execution is the cancellation handle of one live MessageProcessor run.
type execution struct {
	// cancel cancels the ctx passed to ProcessMessage.
	cancel context.CancelFunc
	// cancelRequested records that OnCancelTask asked for cancellation, so the
	// engine's close rule marks the task CANCELED when the MessageProcessor closes the
	// channel without a terminal state of its own.
	cancelRequested atomic.Bool
	// pipe is the message/stream response pipe (nil for unary requests). It is
	// kept on the handle so manager Close can end every in-flight stream and
	// unblock an engine parked on a blocking pipe send.
	pipe *taskSubscriber
	// yieldDone is non-nil while a suspended round publishes its last event.
	// A continuation waits for it instead of being rejected as concurrent, so
	// every observer can safely react to the suspend frame immediately.
	yieldDone chan struct{}
}

// sendOutcome is a unary result candidate: exactly one of the fields is set.
type sendOutcome struct {
	task    *protocol.Task
	message *protocol.Message
}

// engineStopReason records why this round stopped applying events. The engine
// still drains the processor channel after stopping so the producer cannot
// leak on a blocked send.
type engineStopReason uint8

const (
	engineStopNone engineStopReason = iota
	engineStopTerminal
	engineStopViolation
	engineStopYield
)

func (reason engineStopReason) description() string {
	switch reason {
	case engineStopTerminal:
		return "a terminal state"
	case engineStopViolation:
		return "a contract violation"
	case engineStopYield:
		return "the round yielded (suspended task)"
	default:
		return "an unknown stop reason"
	}
}

// engine drains one MessageProcessor event channel. It persists every event before
// broadcasting it (§3.6: GetTask must never lag what a subscriber has seen),
// applies the close rules (§3.5) at end of stream, and always drains the
// channel fully so a sending MessageProcessor can never block on a gone consumer.
type engine struct {
	manager *TaskManager
	ec      *taskmanager.ExecContext
	exec    *execution
	// pipe is the message/stream response channel; nil for unary requests.
	pipe *taskSubscriber

	// Drain lifecycle. Only run() and its callees access these fields.
	stopReason engineStopReason
	// pendingInlinePush is true for a fresh request carrying an inline config.
	// The config is registered only when the first task event materializes the
	// task, so processor startup failures and pure-Message replies leave no orphan.
	pendingInlinePush bool

	// Unary result derivation.
	// taskWritten records whether THIS round wrote to the task (lazy create,
	// status/artifact persist, violation or close-rule write). §3.1 keys the
	// unary result on it: a continuation round that only emits Messages
	// answers with the last Message, not the untouched task snapshot.
	taskWritten bool
	// finalOutcome is the task snapshot or Message returned to a blocking unary
	// caller after done closes. A yielded task is captured before ownership can
	// pass to a continuation.
	finalOutcome sendOutcome

	// Conversation-history deduplication.
	// lastRolledStatusMessage is compared by identity so a message reused across
	// several status updates is not appended to history more than once.
	lastRolledStatusMessage *protocol.Message

	// Immediate-result synchronization.
	// immediateResult carries the immediate result (first persisted task snapshot
	// or first Message) to a returnImmediately waiter. Buffered with 1 slot and
	// written at most once, so the engine never blocks on it.
	immediateSent   bool
	immediateResult chan sendOutcome

	// Final-result synchronization.
	// done is closed when the engine has finished: close rules applied, pipe
	// closed, execution deregistered, and finalOutcome written.
	done chan struct{}
}

// prepareExecContext runs the request preparation shared by OnSendMessage and
// OnSendMessageStream: it registers exec as the task's single live run, then
// resolves and validates the continuation task (if any), and only then stamps
// and stores the incoming message and builds the read-only ExecContext — a
// rejected request never reaches the MessageProcessor and leaves no trace.
func (m *TaskManager) prepareExecContext(
	ctx context.Context,
	request *protocol.SendMessageParams,
	exec *execution,
	taskID string,
) (*taskmanager.ExecContext, error) {
	message := &request.Message
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}

	// Register-or-reject atomically under the registry lock FIRST: a task
	// admits at most one live run, or concurrent rounds would interleave their
	// writes and close rules. Registering before the continuation load also
	// orders the load after the previous round's (or a no-live cancel's) last
	// write — the slot frees only after that write — so the snapshot below can
	// never be a stale pre-terminal copy that would smuggle writes past a
	// terminal state.
	if err := m.registerExecution(ctx, taskID, exec); err != nil {
		return nil, err
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			m.releaseExecution(taskID, exec)
		}
	}()

	// Continuation: a message carrying a taskId targets an existing task (§3.4).
	taskCopy, err := m.resolveContinuation(message)
	if err != nil {
		return nil, err
	}

	// A follow-up without an explicit contextId continues the task's
	// conversation; otherwise ec.History would miss the earlier turns.
	if taskCopy != nil && (message.ContextID == nil || *message.ContextID == "") && taskCopy.ContextID != "" {
		contextID := taskCopy.ContextID
		message.ContextID = &contextID
	}

	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}

	// Finish every fallible validation before recording the request. Rejected
	// requests must not clear a continuation's current status message or enter
	// the conversation history.
	acceptedOutputModes, pushConfig, err := m.prepareExecConfiguration(
		request.Configuration,
		taskID,
		taskCopy != nil,
	)
	if err != nil {
		return nil, err
	}

	m.commitIncomingMessage(taskID, message, taskCopy)
	// The manager's history limit shapes the processor snapshot; the request's
	// historyLength only shapes the task returned to the client.
	history := m.getConversationHistory(*message.ContextID, m.options.MaxHistoryLength)

	releaseOnError = false
	return &taskmanager.ExecContext{
		TaskID:              taskID,
		Task:                taskCopy,
		Message:             *message,
		ContextID:           *message.ContextID,
		Tenant:              request.Tenant,
		History:             history,
		AcceptedOutputModes: acceptedOutputModes,
		PushConfig:          pushConfig,
	}, nil
}

// prepareExecConfiguration snapshots the request configuration and validates
// its inline push registration. For an existing task the registration can be
// saved immediately; for a lazy new task the engine defers it until the first
// task event materializes the task.
func (m *TaskManager) prepareExecConfiguration(
	configuration *protocol.SendMessageConfiguration,
	taskID string,
	continuation bool,
) ([]string, *protocol.TaskPushNotificationConfig, error) {
	if configuration == nil {
		return nil, nil, nil
	}

	acceptedOutputModes := append([]string(nil), configuration.AcceptedOutputModes...)
	if configuration.PushConfig == nil {
		return acceptedOutputModes, nil, nil
	}
	if !m.pushEnabled {
		return nil, nil, taskmanager.ErrPushNotificationNotSupported()
	}

	pushConfig := clonePushConfig(*configuration.PushConfig)
	pushConfig.TaskID = taskID
	if pushConfig.ID == "" {
		// Inline registration retains the legacy stable default ID so a
		// continuation updates the same webhook rather than multiplying it.
		pushConfig.ID = taskID
	}
	if err := push.ValidateConfig(pushConfig); err != nil {
		return nil, nil, taskmanager.ErrInvalidParams(err.Error())
	}
	if continuation {
		stored, err := m.pushStore.save(pushConfig)
		if err != nil {
			return nil, nil, err
		}
		pushConfig = stored
	}
	return acceptedOutputModes, &pushConfig, nil
}

// commitIncomingMessage advances conversation history after all request
// validation has succeeded. On a continuation, the suspended status message
// becomes history immediately before the new user turn.
func (m *TaskManager) commitIncomingMessage(
	taskID string,
	message *protocol.Message,
	taskCopy *protocol.Task,
) {
	// A current status message remains on Task.Status until the task advances.
	// A follow-up advances the conversation: move that message into history
	// before the new user turn, and clear it from the current status so one Task
	// snapshot never exposes the same message in both places.
	if taskCopy != nil && taskCopy.Status.Message != nil {
		statusMessage := taskCopy.Status.Message
		taskCopy.Status.Message = nil
		m.taskMu.Lock()
		if stored, exists := m.tasks[taskID]; exists {
			stored.Status.Message = nil
		}
		m.taskMu.Unlock()
		m.storeStatusMessage(taskID, *message.ContextID, statusMessage)
	}
	m.storeMessage(*message)
}

// resolveContinuation loads and validates the task a continuation message
// targets (§3.4), returning a copy for ec.Task. A message without a taskId is
// a fresh round and resolves to nil. When taskId is set it is the same value
// the caller registered the execution under (see startExecution).
func (m *TaskManager) resolveContinuation(message *protocol.Message) (*protocol.Task, error) {
	if message.TaskID == nil || *message.TaskID == "" {
		return nil, nil
	}
	taskID := *message.TaskID

	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	stored, exists := m.tasks[taskID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(taskID)
	}
	if isFinalState(stored.Status.State) {
		// A terminal task is immutable: reject without invoking the MessageProcessor.
		return nil, taskmanager.ErrInvalidParams(
			fmt.Sprintf("task %s is in terminal state %s", taskID, stored.Status.State))
	}
	if message.ContextID != nil && *message.ContextID != "" && *message.ContextID != stored.ContextID {
		// A continuation must stay in the task's own conversation: a foreign
		// contextId would resolve the wrong ec.History and contradict the
		// task snapshot's ContextID.
		return nil, taskmanager.ErrInvalidParams(fmt.Sprintf("message contextId does not match task %s context", taskID))
	}
	return copyTask(stored), nil
}

// startExecution prepares the ExecContext, invokes the MessageProcessor, and starts
// the drain engine in its own goroutine (never in the request goroutine).
// withPipe selects the streaming shape of the request.
func (m *TaskManager) startExecution(
	reqCtx context.Context,
	request *protocol.SendMessageParams,
	withPipe bool,
) (*engine, error) {
	// The execution ctx is detached from the request ctx's cancellation but
	// keeps its values: a client disconnect must not cancel the work (§3.3).
	// Only OnCancelTask (and manager teardown) cancels it, via the registry.
	execCtx, cancel := context.WithCancel(detachedCtx{parent: reqCtx})
	exec := &execution{cancel: cancel}

	// ec.TaskID is always pre-allocated; whether a task comes into existence
	// is up to the MessageProcessor (§3.2 lazy creation).
	taskID := ""
	if request.Message.TaskID != nil && *request.Message.TaskID != "" {
		taskID = *request.Message.TaskID
	} else {
		taskID = protocol.GenerateTaskID()
	}
	// The pipe is created before registration so manager Close, which walks
	// the registry, always sees it.
	if withPipe {
		exec.pipe = newTaskSubscriber(
			taskID,
			m.options.TaskSubscriberBufSize,
			m.options.TaskSubscriberBlockingSend,
		)
	}

	// prepareExecContext registers exec as the task's single live run before
	// the MessageProcessor is invoked, so OnCancelTask can reach the run from
	// the very first instant events may be produced.
	ec, err := m.prepareExecContext(reqCtx, request, exec, taskID)
	if err != nil {
		cancel()
		return nil, err
	}

	events, err := m.processor.ProcessMessage(execCtx, ec)
	if err != nil {
		// Failed start (§3.5): no events are consumed, nothing was persisted.
		m.releaseExecution(ec.TaskID, exec)
		cancel()
		return nil, err
	}
	if events == nil {
		m.releaseExecution(ec.TaskID, exec)
		cancel()
		return nil, taskmanager.ErrInternalError("processor returned nil channel")
	}

	eng := &engine{
		manager:           m,
		ec:                ec,
		exec:              exec,
		pipe:              exec.pipe,
		pendingInlinePush: ec.PushConfig != nil && ec.Task == nil,
		immediateResult:   make(chan sendOutcome, 1),
		done:              make(chan struct{}),
	}
	go func() {
		defer m.engineWg.Done()
		eng.run(events)
	}()
	return eng, nil
}

// registerExecution publishes the cancellation handle of a starting run. A
// task admits at most one live run; a continuation waits through the previous
// round's short suspend handoff, while any other concurrent round is rejected.
func (m *TaskManager) registerExecution(ctx context.Context, taskID string, exec *execution) error {
	for {
		m.execMu.Lock()
		if m.closed {
			m.execMu.Unlock()
			return taskmanager.ErrInternalError("task manager is closed")
		}
		current, exists := m.executions[taskID]
		if !exists {
			m.executions[taskID] = exec
			// Counted under the registry lock so Close (which flips m.closed first)
			// can never begin waiting before a just-admitted run is counted.
			m.engineWg.Add(1)
			m.execMu.Unlock()
			return nil
		}
		yieldDone := current.yieldDone
		m.execMu.Unlock()
		if yieldDone == nil {
			return taskmanager.ErrInvalidParams(
				fmt.Sprintf("task %s already has an active execution", taskID))
		}
		select {
		case <-yieldDone:
			// The previous round has finished publishing its suspend event. Retry
			// under the lock so Close or another continuation can win cleanly.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// releaseExecution aborts a registered run whose engine never started: it
// undoes registerExecution's registration and engine count.
func (m *TaskManager) releaseExecution(taskID string, exec *execution) {
	m.deregisterExecution(taskID, exec)
	m.engineWg.Done()
}

// claimCancelSlot atomically returns the task's live run or — when there is
// none — claims the execution slot with a sentinel, so a no-live cancel's
// CANCELED write gets the same single-writer guarantee as a run: no
// continuation can register (and then write) concurrently with it.
func (m *TaskManager) claimCancelSlot(
	taskID string,
) (live *execution, sentinel *execution, yieldDone <-chan struct{}) {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if exec, ok := m.executions[taskID]; ok {
		if exec.yieldDone != nil {
			return nil, nil, exec.yieldDone
		}
		return exec, nil, nil
	}
	sentinel = &execution{cancel: func() {}}
	m.executions[taskID] = sentinel
	return nil, sentinel, nil
}

// requestExecutionCancel linearizes an accepted cancellation against a
// suspend handoff. It returns the handoff channel when yield won, or accepted
// after publishing cancelRequested under the registry lock.
func (m *TaskManager) requestExecutionCancel(
	taskID string,
	exec *execution,
) (yieldDone <-chan struct{}, accepted bool) {
	m.execMu.Lock()
	if m.executions[taskID] != exec {
		m.execMu.Unlock()
		return nil, false
	}
	if exec.yieldDone != nil {
		yieldDone = exec.yieldDone
		m.execMu.Unlock()
		return yieldDone, false
	}
	exec.cancelRequested.Store(true)
	m.execMu.Unlock()
	exec.cancel()
	return nil, true
}

// deregisterExecution removes the handle if it still belongs to this run.
func (m *TaskManager) deregisterExecution(taskID string, exec *execution) {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if m.executions[taskID] == exec {
		delete(m.executions, taskID)
		if exec.yieldDone != nil {
			close(exec.yieldDone)
			exec.yieldDone = nil
		}
	}
}

// beginExecutionYield turns an active slot into a short handoff barrier. The
// slot remains owned by exec until its suspend frame has reached every local
// observer, but continuations wait for the handoff instead of failing.
func (m *TaskManager) beginExecutionYield(taskID string, exec *execution) bool {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if m.executions[taskID] == exec && exec.yieldDone == nil && !exec.cancelRequested.Load() {
		exec.yieldDone = make(chan struct{})
		return true
	}
	return false
}

// abortExecutionYield restores an active slot when committing the suspended
// state fails. Waiters wake and re-evaluate it as an ordinary active run.
func (m *TaskManager) abortExecutionYield(taskID string, exec *execution) {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if m.executions[taskID] == exec && exec.yieldDone != nil {
		close(exec.yieldDone)
		exec.yieldDone = nil
	}
}

// liveExecution returns the cancellation handle of the task's live run, if any.
func (m *TaskManager) liveExecution(taskID string) *execution {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	return m.executions[taskID]
}

// run consumes the MessageProcessor channel until it closes. After a terminal state or
// a contract violation the remaining events are drained and discarded (§3.3:
// the framework never stops reading, so senders never leak).
func (eng *engine) run(events <-chan protocol.StreamEvent) {
	defer eng.finish()
	for event := range events {
		if eng.stopReason != engineStopNone {
			log.Warnf("memory TaskManager: discarding %T for task %s emitted after %s",
				event, eng.ec.TaskID, eng.stopReason.description())
			continue
		}
		switch e := event.(type) {
		case *protocol.Message:
			if e == nil {
				log.Warnf("memory TaskManager: ignoring nil Message event for task %s", eng.ec.TaskID)
				continue
			}
			eng.handleMessage(e)
		case *protocol.TaskStatusUpdateEvent:
			if e == nil {
				log.Warnf("memory TaskManager: ignoring nil status event for task %s", eng.ec.TaskID)
				continue
			}
			if eng.stampTaskEvent(&e.TaskID, &e.ContextID) {
				eng.handleStatus(e)
			}
		case *protocol.TaskArtifactUpdateEvent:
			if e == nil {
				log.Warnf("memory TaskManager: ignoring nil artifact event for task %s", eng.ec.TaskID)
				continue
			}
			if eng.stampTaskEvent(&e.TaskID, &e.ContextID) {
				eng.handleArtifact(e)
			}
		case *protocol.Task:
			// §3.2: task snapshots are materialized by the framework from the
			// event stream; emitting one is a contract violation.
			eng.violate("processor emitted forbidden Task snapshot")
		default:
			log.Warnf("memory TaskManager: ignoring unknown event %T for task %s", event, eng.ec.TaskID)
		}
	}
}

// stampTaskEvent fills empty event IDs from the ExecContext and rejects
// foreign IDs (§3.2): one ProcessMessage may only drive its own task.
func (eng *engine) stampTaskEvent(taskID, contextID *string) bool {
	if *taskID == "" {
		*taskID = eng.ec.TaskID
	} else if *taskID != eng.ec.TaskID {
		eng.violate("processor emitted event for foreign task")
		return false
	}
	if *contextID == "" {
		*contextID = eng.ec.ContextID
	} else if *contextID != eng.ec.ContextID {
		eng.violate("processor emitted event for foreign task")
		return false
	}
	return true
}

// checkMessageIDs reports whether a Message event addresses this execution:
// TaskID/ContextID may be empty (the framework stamps them) but must not name
// a foreign task or context — one ProcessMessage may only drive its own round.
func (eng *engine) checkMessageIDs(msg *protocol.Message) bool {
	if msg.TaskID != nil && *msg.TaskID != "" && *msg.TaskID != eng.ec.TaskID {
		return false
	}
	if msg.ContextID != nil && *msg.ContextID != "" && *msg.ContextID != eng.ec.ContextID {
		return false
	}
	return true
}

// handleMessage processes a direct reply: stamp and store it into the
// conversation, remember it as the unary fallback result, and deliver it. A
// pure message creates no task (§3.2), so task subscribers are only notified
// when a task already exists (e.g. a continuation with live resubscribers).
func (eng *engine) handleMessage(message *protocol.Message) {
	if !eng.checkMessageIDs(message) {
		eng.violate("processor emitted event for foreign task")
		return
	}
	m := eng.manager
	contextID := eng.ec.ContextID
	m.processReplyMessage(&contextID, message)
	eng.finalOutcome = sendOutcome{message: message}
	eng.offerImmediateResult(sendOutcome{message: message})

	response := protocol.NewStreamResponseMessage(message)
	eng.sendToPipe(response)

	m.taskMu.RLock()
	_, exists := m.tasks[eng.ec.TaskID]
	m.taskMu.RUnlock()
	if exists {
		m.notifySubscribers(eng.ec.TaskID, response)
	}
}

// storeStatusMessage stores a stamped copy without mutating the processor's
// message, which may still be visible in an earlier task snapshot or wire event.
func (m *TaskManager) storeStatusMessage(taskID, contextID string, message *protocol.Message) {
	if message == nil {
		return
	}
	stored := *message
	stored.ContextID = &contextID
	stored.TaskID = &taskID
	stored.Role = protocol.MessageRoleAgent
	if stored.MessageID == "" {
		stored.MessageID = protocol.GenerateMessageID()
	}
	m.storeMessage(stored)
}

// rollStatusMessage moves a superseded status message into conversation
// history. The current status message stays on Task.Status until the next
// status transition or follow-up user message, matching the reference SDKs.
// Pointer checks avoid redundant work within a round; storeMessage provides the
// final MessageID-based idempotency guard.
func (eng *engine) rollStatusMessage(message *protocol.Message) {
	if message == nil ||
		message == eng.lastRolledStatusMessage ||
		message == eng.finalOutcome.message {
		return
	}
	eng.lastRolledStatusMessage = message
	eng.manager.storeStatusMessage(eng.ec.TaskID, eng.ec.ContextID, message)
}

// handleStatus persists a status update and then broadcasts it. The first task
// event materializes the task (§3.2 lazy creation). A terminal state closes
// the fan-out subscribers but the channel keeps being drained.
func (eng *engine) handleStatus(event *protocol.TaskStatusUpdateEvent) {
	m := eng.manager
	if event.Status.State == "" || event.Status.State == protocol.TaskStateUnspecified {
		// A stateless status event would strand the task in a dangling
		// non-terminal state (never TTL-collected, resubscribable forever).
		eng.violate("status event without a state")
		return
	}
	if event.Status.Timestamp == "" {
		event.Status.Timestamp = nowTimestamp()
	}
	final := isFinalState(event.Status.State)
	suspended := !final && isSuspendedState(event.Status.State)
	event.Final = final
	yielding := false
	if suspended {
		// Establish the handoff before the suspended state can become visible in
		// the store. A polling client may otherwise observe INPUT_REQUIRED and
		// still be rejected as a concurrent execution. If cancellation claimed
		// the slot first, keep this round active so its close rule can persist
		// CANCELED rather than swallowing the accepted cancellation.
		yielding = m.beginExecutionYield(eng.ec.TaskID, eng.exec)
	}

	// Persist first (§3.6), under the task lock.
	m.taskMu.Lock()
	task, exists := m.tasks[eng.ec.TaskID]
	if exists && isFinalState(task.Status.State) {
		// Terminal states are immutable: never write over one, whoever set it.
		m.taskMu.Unlock()
		if yielding {
			m.abortExecutionYield(eng.ec.TaskID, eng.exec)
		}
		log.Warnf("memory TaskManager: discarding status %s for task %s already in terminal state %s",
			event.Status.State, eng.ec.TaskID, task.Status.State)
		eng.stopReason = engineStopTerminal
		return
	}
	var previousStatusMessage *protocol.Message
	if !exists {
		task = eng.newTask(event.Status)
		m.tasks[eng.ec.TaskID] = task
	} else {
		previousStatusMessage = task.Status.Message
		task.Status = event.Status
	}
	eng.taskWritten = true
	snapshot := eng.immediateSnapshotLocked(task)
	var yieldOutcome sendOutcome
	if yielding {
		// The round is about to yield ownership: keep its own copy as the unary
		// result, since the shared entry may belong to a continuation before
		// finish() runs.
		yieldOutcome.task = copyTask(task)
	}
	m.taskMu.Unlock()
	if err := eng.persistInlinePushConfig(); err != nil {
		if yielding {
			m.abortExecutionYield(eng.ec.TaskID, eng.exec)
		}
		eng.violate(err.Error())
		return
	}

	// The old status message has now been superseded. Move it to history before
	// publishing the new status; the new/current message remains only on Status.
	eng.rollStatusMessage(previousStatusMessage)

	response := protocol.NewStreamResponseStatusUpdate(event)
	if yielding {
		eng.finalOutcome = yieldOutcome
		eng.stopReason = engineStopYield
		// Queue automatic push before exposing the suspend state. A full bounded
		// queue may delay publication, but a client can never observe a state it
		// cannot yet continue. The handoff starts before enqueue so a client that
		// polls the persisted task also waits instead of being rejected. It also
		// serializes task-subscriber fan-out with a continuation round.
		m.dispatchPush(eng.ec.TaskID, response)
		eng.broadcastWithoutPush(response, snapshot)
		eng.closePipe()
		m.deregisterExecution(eng.ec.TaskID, eng.exec)
		return
	}

	// Then broadcast: any subscriber that sees this event is guaranteed to
	// find the store at least as fresh via GetTask.
	eng.broadcast(response, snapshot)

	if final {
		eng.stopReason = engineStopTerminal
		m.cleanSubscribers(eng.ec.TaskID)
		// Nothing can follow a terminal frame: end the response stream here
		// instead of trusting the MessageProcessor to close its channel promptly.
		eng.closePipe()
	}
}

// handleArtifact persists an artifact chunk and then broadcasts it. An
// artifact arriving before any status event still materializes the task, in
// the submitted state.
func (eng *engine) handleArtifact(event *protocol.TaskArtifactUpdateEvent) {
	m := eng.manager

	m.taskMu.Lock()
	task, exists := m.tasks[eng.ec.TaskID]
	if exists && isFinalState(task.Status.State) {
		// Terminal states are immutable: no artifact may land after one.
		m.taskMu.Unlock()
		log.Warnf("memory TaskManager: discarding artifact for task %s already in terminal state %s",
			eng.ec.TaskID, task.Status.State)
		eng.stopReason = engineStopTerminal
		return
	}
	if !exists {
		task = eng.newTask(protocol.TaskStatus{
			State:     protocol.TaskStateSubmitted,
			Timestamp: nowTimestamp(),
		})
		m.tasks[eng.ec.TaskID] = task
	}
	var appendedAsNew bool
	task.Artifacts, appendedAsNew = protocol.AppendArtifact(task.Artifacts, event.Artifact, event.Append != nil && *event.Append)
	if appendedAsNew {
		log.Warnf("memory TaskManager: artifact %s for task %s used append=true with no prior chunk; stored as a new artifact",
			event.Artifact.ArtifactID, eng.ec.TaskID)
	}
	eng.taskWritten = true
	snapshot := eng.immediateSnapshotLocked(task)
	m.taskMu.Unlock()
	if err := eng.persistInlinePushConfig(); err != nil {
		eng.violate(err.Error())
		return
	}

	eng.broadcast(protocol.NewStreamResponseArtifactUpdate(event), snapshot)
}

func (eng *engine) persistInlinePushConfig() error {
	if !eng.pendingInlinePush {
		return nil
	}
	eng.pendingInlinePush = false
	if _, err := eng.manager.pushStore.save(*eng.ec.PushConfig); err != nil {
		return fmt.Errorf("persist inline push config: %w", err)
	}
	return nil
}

// newTask materializes the task on its first event (§3.2 lazy creation),
// seeded the same way BuildTask used to seed it.
func (eng *engine) newTask(status protocol.TaskStatus) *protocol.Task {
	return &protocol.Task{
		ID:        eng.ec.TaskID,
		ContextID: eng.ec.ContextID,
		Status:    status,
		Artifacts: make([]protocol.Artifact, 0),
		History:   make([]protocol.Message, 0),
		Metadata:  make(map[string]interface{}),
	}
}

// immediateSnapshotLocked returns a copy of the task to serve as the
// immediate result (returnImmediately), or nil once one was already taken.
// The caller must hold taskMu so the snapshot equals what was just persisted.
func (eng *engine) immediateSnapshotLocked(task *protocol.Task) *protocol.Task {
	if eng.immediateSent {
		return nil
	}
	return copyTask(task)
}

// broadcast delivers an already-persisted task event: request pipe first, then
// task subscribers. snapshot, when non-nil, resolves a returnImmediately wait.
func (eng *engine) broadcast(response protocol.StreamResponse, snapshot *protocol.Task) {
	if snapshot != nil {
		eng.offerImmediateResult(sendOutcome{task: snapshot})
	}
	eng.sendToPipe(response)
	eng.manager.notifySubscribers(eng.ec.TaskID, response)
}

// broadcastWithoutPush publishes an event whose automatic push delivery was
// already enqueued by the caller.
func (eng *engine) broadcastWithoutPush(response protocol.StreamResponse, snapshot *protocol.Task) {
	if snapshot != nil {
		eng.offerImmediateResult(sendOutcome{task: snapshot})
	}
	eng.sendToPipe(response)
	eng.manager.notifyLiveSubscribers(eng.ec.TaskID, response)
}

// offerImmediateResult publishes the immediate result exactly once.
func (eng *engine) offerImmediateResult(out sendOutcome) {
	if eng.immediateSent {
		return
	}
	eng.immediateSent = true
	eng.immediateResult <- out // buffered, single write: never blocks
}

// sendToPipe forwards an event to the streaming request pipe, if any.
func (eng *engine) sendToPipe(response protocol.StreamResponse) {
	if eng.pipe == nil {
		return
	}
	if err := eng.pipe.Send(response); err != nil {
		log.Warnf("memory TaskManager: dropping stream event for task %s: %v", eng.ec.TaskID, err)
	}
}

// closePipe ends the message/stream response stream, if any. Subscriber close
// is CAS-guarded, so calling it from more than one place is safe.
func (eng *engine) closePipe() {
	if eng.pipe != nil {
		eng.pipe.Close()
	}
}

// violate handles a contract violation: log it, mark the task FAILED when one
// exists and is not terminal yet, and stop applying events. The remaining
// stream is drained and discarded by run().
func (eng *engine) violate(reason string) {
	log.Errorf("memory TaskManager: contract violation on task %s: %s", eng.ec.TaskID, reason)
	eng.stopReason = engineStopViolation

	m := eng.manager
	event := eng.statusEvent(protocol.TaskStateFailed, eng.failureStatusMessage(reason))
	m.taskMu.Lock()
	task, exists := m.tasks[eng.ec.TaskID]
	if !exists || isFinalState(task.Status.State) {
		m.taskMu.Unlock()
		return
	}
	task.Status = event.Status
	eng.taskWritten = true
	// The framework-written FAILED is a task event (§3.1): offer it as the
	// immediateResult outcome so a returnImmediately caller is not left waiting for
	// the violating processor to close its channel.
	snapshot := eng.immediateSnapshotLocked(task)
	m.taskMu.Unlock()

	eng.broadcast(protocol.NewStreamResponseStatusUpdate(event), snapshot)
	m.cleanSubscribers(eng.ec.TaskID)
	eng.closePipe()
}

// finish applies the §3.5 close rules once the MessageProcessor channel is closed,
// then closes the request pipe, deregisters the execution, and signals unary
// waiters through done.
func (eng *engine) finish() {
	m := eng.manager

	if eng.stopReason == engineStopYield {
		// The round yielded at suspend (§3.4): ownership moved on — a
		// continuation or a no-live cancel may already be writing the task, so
		// no close rule may touch it. finalOutcome already holds the suspend-time
		// snapshot; the pipe closed and the slot was freed at yield.
		eng.exec.cancel() // release the detached ctx resources
		close(eng.done)
		return
	}

	var closing *protocol.TaskStatusUpdateEvent

	m.taskMu.Lock()
	task, exists := m.tasks[eng.ec.TaskID]
	if exists && !isFinalState(task.Status.State) {
		switch {
		case eng.exec.cancelRequested.Load():
			// Cancel was requested and the MessageProcessor closed without a terminal
			// state of its own: the framework marks CANCELED. Had the MessageProcessor
			// emitted completed/failed after the cancel, that persist already
			// happened in handleStatus and wins (§3.3) — this branch is then
			// never reached because the task is terminal.
			closing = eng.statusEvent(protocol.TaskStateCanceled, nil)
			task.Status = closing.Status
		case !eng.taskWritten:
			// This round never wrote to the task: it must not apply close rules
			// to state some other round left behind (§3.5 is round-scoped).
		case task.Status.State == protocol.TaskStateSubmitted ||
			task.Status.State == protocol.TaskStateWorking:
			// Finishing without a conclusion is a MessageProcessor bug: fail explicitly
			// rather than pretending completion (§3.5).
			closing = eng.statusEvent(protocol.TaskStateFailed,
				eng.failureStatusMessage("processor finished without terminal state"))
			task.Status = closing.Status
		default:
			// input-required / auth-required: a normal end; the task stays
			// suspended awaiting a follow-up message (§3.4).
		}
	}
	if closing != nil {
		eng.taskWritten = true
	}
	// §3.1: only rounds that wrote to the task answer with a Task snapshot; a
	// continuation that merely replied with Messages answers with the Message.
	if exists && eng.taskWritten {
		eng.finalOutcome = sendOutcome{task: copyTask(task)}
	}
	m.taskMu.Unlock()

	if closing != nil {
		response := protocol.NewStreamResponseStatusUpdate(closing)
		eng.sendToPipe(response)
		m.notifySubscribers(eng.ec.TaskID, response)
		m.cleanSubscribers(eng.ec.TaskID)
	}

	if eng.pipe != nil {
		eng.pipe.Close()
	}
	eng.exec.cancel() // release the detached ctx resources
	m.deregisterExecution(eng.ec.TaskID, eng.exec)
	close(eng.done)
}

// statusEvent builds a framework-originated status update for this execution.
func (eng *engine) statusEvent(state protocol.TaskState, message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return &protocol.TaskStatusUpdateEvent{
		TaskID:    eng.ec.TaskID,
		ContextID: eng.ec.ContextID,
		Status: protocol.TaskStatus{
			State:     state,
			Message:   message,
			Timestamp: nowTimestamp(),
		},
		Final: isFinalState(state),
	}
}

// failureStatusMessage builds the agent message attached to framework-marked
// FAILED statuses so clients can see why the task failed.
func (eng *engine) failureStatusMessage(text string) *protocol.Message {
	message := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart(text)})
	contextID := eng.ec.ContextID
	taskID := eng.ec.TaskID
	message.ContextID = &contextID
	message.TaskID = &taskID
	return &message
}

// buildSendResponse derives the unary result (§3.1): the task snapshot when a
// task exists (history trimmed per the request's historyLength), otherwise the
// last message; an execution that produced neither is a MessageProcessor bug.
func (m *TaskManager) buildSendResponse(
	task *protocol.Task,
	message *protocol.Message,
	historyLength *int,
) (*protocol.SendMessageResponse, error) {
	if task != nil {
		m.fillTaskHistory(task, historyLength)
		return protocol.NewSendMessageResponseTask(task), nil
	}
	if message != nil {
		return protocol.NewSendMessageResponseMessage(message), nil
	}
	return nil, taskmanager.ErrInternalError("processor produced no result")
}

// historyLengthFromConfig extracts the response historyLength, nil-safe.
func historyLengthFromConfig(config *protocol.SendMessageConfiguration) *int {
	if config == nil {
		return nil
	}
	return config.HistoryLength
}
