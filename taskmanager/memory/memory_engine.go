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

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
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
}

// sendOutcome is a unary result candidate: exactly one of the fields is set.
type sendOutcome struct {
	task    *protocol.Task
	message *protocol.Message
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

	// Engine-goroutine-local state (only run() and its callees touch these).
	terminal            bool
	violated            bool
	immediateResultSent bool
	// yielded records that this round emitted a suspend state (§3.4) and gave
	// the task up: a continuation may already own it, so later events from this
	// round are discarded and the close rules are skipped.
	yielded bool
	// yieldSnapshot is the task copy taken when the round yielded; it is the
	// round's unary result (§3.1) — the shared store may already belong to a
	// continuation by the time finish() runs.
	yieldSnapshot *protocol.Task
	// taskTouched records whether THIS round wrote to the task (lazy create,
	// status/artifact persist, violation or close-rule write). §3.1 keys the
	// unary result on it: a continuation round that only emits Messages
	// answers with the last Message, not the untouched task snapshot.
	taskTouched bool
	lastMessage *protocol.Message
	// lastStatusMsg is the most recent status message folded into the
	// conversation, compared by identity, so a message reused across several
	// status updates in this round is not appended to the history more than once.
	lastStatusMsg *protocol.Message

	// immediateResult carries the immediate result (first persisted task snapshot
	// or first Message) to a returnImmediately waiter. Buffered with 1 slot and
	// written at most once, so the engine never blocks on it.
	immediateResult chan sendOutcome
	// finalTask is the task snapshot at end of stream; written before done is
	// closed, read by unary waiters only after done is closed.
	finalTask *protocol.Task
	// done is closed when the engine has finished: close rules applied, pipe
	// closed, execution deregistered.
	done chan struct{}
}

// prepareExecContext runs the request preparation shared by OnSendMessage and
// OnSendMessageStream: it registers exec as the task's single live run, then
// resolves and validates the continuation task (if any), and only then stamps
// and stores the incoming message and builds the read-only ExecContext — a
// rejected request never reaches the MessageProcessor and leaves no trace.
func (m *TaskManager) prepareExecContext(
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
	if err := m.registerExecution(taskID, exec); err != nil {
		return nil, err
	}

	// Continuation: a message carrying a taskId targets an existing task (§3.4).
	taskCopy, err := m.resolveContinuation(message)
	if err != nil {
		m.releaseExecution(taskID, exec)
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
	m.storeMessage(*message)

	var acceptedOutputModes []string
	var pushConfig *protocol.TaskPushNotificationConfig
	if request.Configuration != nil {
		acceptedOutputModes = request.Configuration.AcceptedOutputModes
		pushConfig = request.Configuration.PushConfig
	}
	// An inline push config is a registration: reject it when push is not
	// enabled (the client would otherwise wait on a webhook that can never
	// fire), and persist it when it is — so it becomes queryable via the
	// config RPCs and receives automatic deliveries, matching the official
	// SDK. It still reaches the processor as ec.PushConfig either way.
	if pushConfig != nil {
		if m.pushSender == nil {
			m.releaseExecution(taskID, exec)
			return nil, taskmanager.ErrPushNotificationNotSupported()
		}
		cfg := *pushConfig
		cfg.TaskID = taskID
		if cfg.ID == "" {
			// Same replace-the-default rule as OnPushNotificationSet.
			cfg.ID = taskID
		}
		if _, err := m.pushStore.save(cfg); err != nil {
			m.releaseExecution(taskID, exec)
			return nil, err
		}
	}
	return &taskmanager.ExecContext{
		TaskID:    taskID,
		Task:      taskCopy,
		Message:   *message,
		ContextID: *message.ContextID,
		Tenant:    request.Tenant,
		// Snapshot truncated per the manager's own limit; the request's
		// historyLength only shapes the response task, not this snapshot.
		History:             m.getConversationHistory(*message.ContextID, m.options.MaxHistoryLength),
		AcceptedOutputModes: acceptedOutputModes,
		PushConfig:          pushConfig,
	}, nil
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
		return nil, jsonrpc.ErrInvalidParams(
			fmt.Sprintf("task %s is in terminal state %s", taskID, stored.Status.State))
	}
	if message.ContextID != nil && *message.ContextID != "" && *message.ContextID != stored.ContextID {
		// A continuation must stay in the task's own conversation: a foreign
		// contextId would resolve the wrong ec.History and contradict the
		// task snapshot's ContextID.
		return nil, jsonrpc.ErrInvalidParams(
			fmt.Sprintf("message contextId does not match task %s context", taskID))
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
	ec, err := m.prepareExecContext(request, exec, taskID)
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
		return nil, jsonrpc.ErrInternalError("processor returned nil channel")
	}

	eng := &engine{
		manager:         m,
		ec:              ec,
		exec:            exec,
		pipe:            exec.pipe,
		immediateResult: make(chan sendOutcome, 1),
		done:            make(chan struct{}),
	}
	go func() {
		defer m.engineWg.Done()
		eng.run(events)
	}()
	return eng, nil
}

// registerExecution publishes the cancellation handle of a starting run, or
// rejects the round: a task admits at most one live run — concurrent rounds
// would interleave their writes and close rules (and orphan cancel handles).
func (m *TaskManager) registerExecution(taskID string, exec *execution) error {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if m.closed {
		return jsonrpc.ErrInternalError("task manager is closed")
	}
	if _, exists := m.executions[taskID]; exists {
		return jsonrpc.ErrInvalidParams(
			fmt.Sprintf("task %s already has an active execution", taskID))
	}
	m.executions[taskID] = exec
	// Counted under the registry lock so Close (which flips m.closed first)
	// can never begin waiting before a just-admitted run is counted.
	m.engineWg.Add(1)
	return nil
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
func (m *TaskManager) claimCancelSlot(taskID string) (live *execution, sentinel *execution) {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if exec, ok := m.executions[taskID]; ok {
		return exec, nil
	}
	sentinel = &execution{cancel: func() {}}
	m.executions[taskID] = sentinel
	return nil, sentinel
}

// deregisterExecution removes the handle if it still belongs to this run.
func (m *TaskManager) deregisterExecution(taskID string, exec *execution) {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if m.executions[taskID] == exec {
		delete(m.executions, taskID)
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
		if eng.violated || eng.terminal || eng.yielded {
			log.Warnf("memory TaskManager: discarding %T for task %s emitted after %s",
				event, eng.ec.TaskID, eng.stopReason())
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

// stopReason names why the engine stopped applying events (for discard logs).
func (eng *engine) stopReason() string {
	if eng.violated {
		return "a contract violation"
	}
	if eng.yielded {
		return "the round yielded (suspended task)"
	}
	return "a terminal state"
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
	eng.lastMessage = message
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

// rollStatusMessage folds a non-terminal status message (e.g. an input-required
// question) into the conversation history so it survives into the next round's
// context instead of vanishing. It stores a stamped copy (agent role, context/
// task IDs, generated ID if absent) rather than mutating the processor's message
// — the original is already published on the task and on the wire event, so
// mutating it here would race a concurrent GetTask. It skips a message object
// this round already stored — as the reply or as the preceding status update —
// and storeMessage is idempotent by MessageID, so reusing one across events (or
// re-emitting the same ID) never multiplies it in the history.
func (eng *engine) rollStatusMessage(message *protocol.Message) {
	if message == nil || message == eng.lastStatusMsg || message == eng.lastMessage {
		return
	}
	eng.lastStatusMsg = message
	contextID := eng.ec.ContextID
	stored := *message
	stored.ContextID = &contextID
	stored.Role = protocol.MessageRoleAgent
	if stored.MessageID == "" {
		stored.MessageID = protocol.GenerateMessageID()
	}
	if stored.TaskID == nil || *stored.TaskID == "" {
		taskID := eng.ec.TaskID
		stored.TaskID = &taskID
	}
	eng.manager.storeMessage(stored)
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
	event.Final = final

	// Persist first (§3.6), under the task lock.
	m.taskMu.Lock()
	task, exists := m.tasks[eng.ec.TaskID]
	if exists && isFinalState(task.Status.State) {
		// Terminal states are immutable: never write over one, whoever set it.
		m.taskMu.Unlock()
		log.Warnf("memory TaskManager: discarding status %s for task %s already in terminal state %s",
			event.Status.State, eng.ec.TaskID, task.Status.State)
		eng.terminal = true
		return
	}
	if !exists {
		task = eng.newTask(event.Status)
		m.tasks[eng.ec.TaskID] = task
	} else {
		task.Status = event.Status
	}
	eng.taskTouched = true
	snapshot := eng.immediateSnapshotLocked(task)
	suspended := !final && isSuspendedState(event.Status.State)
	if suspended {
		// The round is about to yield ownership: keep its own copy as the unary
		// result, since the shared entry may belong to a continuation before
		// finish() runs.
		eng.yieldSnapshot = copyTask(task)
	}
	m.taskMu.Unlock()

	// Fold a NON-terminal status message into the conversation so a later round's
	// GetMessageHistory / GetTask includes it (e.g. an input-required question).
	// A terminal message is left on status.Message only, not duplicated into
	// history: a2a-python likewise rolls the PREVIOUS status message on the next
	// transition, so a final message never enters history. A message-less status
	// is a no-op.
	if !final {
		eng.rollStatusMessage(event.Status.Message)
	}

	// Then broadcast: any subscriber that sees this event is guaranteed to
	// find the store at least as fresh via GetTask.
	eng.broadcast(protocol.NewStreamResponseStatusUpdate(event), snapshot)

	if final {
		eng.terminal = true
		m.cleanSubscribers(eng.ec.TaskID)
		// Nothing can follow a terminal frame: end the response stream here
		// instead of trusting the MessageProcessor to close its channel promptly.
		eng.closePipe()
	} else if suspended {
		// §3.4: a suspended round has yielded the task back for a follow-up —
		// it is no longer actively working. Free the registry slot NOW rather
		// than at finish(): a streaming (or returnImmediately) client that fires
		// its continuation on receiving this suspend frame would otherwise
		// collide with this still-registered run and be wrongly rejected
		// "already has an active execution". finish()'s pointer-guarded
		// deregister then no-ops, and it never removes a continuation's own
		// fresh registration (the guard checks the entry still belongs to us).
		//
		// Yielding also ends this round's writes: a continuation (or a no-live
		// cancel) may own the task from this instant, so later events from this
		// round are discarded (run loop), the close rules are skipped
		// (finish()), and the response stream ends at the suspend frame.
		eng.yielded = true
		m.deregisterExecution(eng.ec.TaskID, eng.exec)
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
		eng.terminal = true
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
	eng.taskTouched = true
	snapshot := eng.immediateSnapshotLocked(task)
	m.taskMu.Unlock()

	eng.broadcast(protocol.NewStreamResponseArtifactUpdate(event), snapshot)
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
	if eng.immediateResultSent {
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

// offerImmediateResult publishes the immediate result exactly once.
func (eng *engine) offerImmediateResult(out sendOutcome) {
	if eng.immediateResultSent {
		return
	}
	eng.immediateResultSent = true
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
	eng.violated = true

	m := eng.manager
	event := eng.statusEvent(protocol.TaskStateFailed, eng.failureStatusMessage(reason))
	m.taskMu.Lock()
	task, exists := m.tasks[eng.ec.TaskID]
	if !exists || isFinalState(task.Status.State) {
		m.taskMu.Unlock()
		return
	}
	task.Status = event.Status
	eng.taskTouched = true
	// The framework-written FAILED is a task event (§3.1): offer it as the
	// immediateResult outcome so a returnImmediately caller is not left waiting for
	// the violating processor to close its channel.
	snapshot := eng.immediateSnapshotLocked(task)
	m.taskMu.Unlock()

	eng.terminal = true
	eng.broadcast(protocol.NewStreamResponseStatusUpdate(event), snapshot)
	m.cleanSubscribers(eng.ec.TaskID)
	eng.closePipe()
}

// finish applies the §3.5 close rules once the MessageProcessor channel is closed,
// then closes the request pipe, deregisters the execution, and signals unary
// waiters through done.
func (eng *engine) finish() {
	m := eng.manager

	if eng.yielded {
		// The round yielded at suspend (§3.4): ownership moved on — a
		// continuation or a no-live cancel may already be writing the task, so
		// no close rule may touch it. The unary result is the suspend-time
		// snapshot; the pipe closed and the slot was freed at yield.
		eng.finalTask = eng.yieldSnapshot
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
		case !eng.taskTouched:
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
		eng.taskTouched = true
	}
	// §3.1: only rounds that wrote to the task answer with a Task snapshot; a
	// continuation that merely replied with Messages answers with the Message.
	if exists && eng.taskTouched {
		eng.finalTask = copyTask(task)
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
	return nil, jsonrpc.ErrInternalError("processor produced no result")
}

// historyLengthFromConfig extracts the response historyLength, nil-safe.
func historyLengthFromConfig(config *protocol.SendMessageConfiguration) *int {
	if config == nil {
		return nil
	}
	return config.HistoryLength
}
