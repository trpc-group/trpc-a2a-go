// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides a Redis-based implementation of the A2A TaskManager interface.
package redis

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// detachedCtx detaches the MessageProcessor's context from the request context's
// cancellation and deadline while keeping its values: a client disconnect
// must NOT cancel the execution (the work keeps running and its results stay
// retrievable). Go 1.20 has no context.WithoutCancel, hence this type.
type detachedCtx struct {
	parent context.Context
}

// Deadline implements context.Context: the detached context never expires.
func (detachedCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done implements context.Context: the detached context is never canceled.
func (detachedCtx) Done() <-chan struct{} { return nil }

// Err implements context.Context: the detached context carries no error.
func (detachedCtx) Err() error { return nil }

// Value implements context.Context: request values pass through.
func (c detachedCtx) Value(key any) any { return c.parent.Value(key) }

// liveExecution is the manager-side handle of a running Execute call. It is
// registered under the task ID before Execute is invoked so that
// OnCancelTask can cancel the MessageProcessor's context, and deregistered when the
// engine finishes.
type liveExecution struct {
	cancel context.CancelFunc
	// cancelRequested distinguishes a cancellation-triggered channel close
	// (task marked CANCELED) from an processor finishing in submitted/working
	// without a conclusion (task marked FAILED).
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

// requestCancel records the cancel request and cancels the MessageProcessor context.
func (le *liveExecution) requestCancel() {
	le.cancelRequested.Store(true)
	le.cancel()
}

// engine consume modes: after a terminal event, a contract violation, or a
// yield (suspend state, §3.4 — ownership moved to a possible continuation)
// the channel is still drained to closure (senders never leak) but events are
// discarded.
const (
	engineConsuming = iota
	engineDrainTerminal
	engineDrainViolation
	engineDrainYielded
	engineDrainMessage
)

// sendOutcome is a message-or-task result candidate for message/send: the
// first immediateResult event (returnImmediately) or the end-of-stream derivation.
type sendOutcome struct {
	task    *protocol.Task
	message *protocol.Message
}

// execution is one round of the MessageProcessor event contract: it consumes the
// event channel, persists every task event before broadcasting it, and
// derives the unary result from the stream.
type execution struct {
	manager *TaskManager
	ec      *taskmanager.ExecContext
	live    *liveExecution
	// owner is resolved once from the request context and frozen for every
	// foreground and detached operation performed by this execution.
	owner string

	// task is the engine's working snapshot; nil until the first task event
	// creates the task (lazy creation: a pure-Message exchange leaves no
	// task behind). On a continuation it starts as a copy of the stored task.
	task *protocol.Task
	// lastMessage is the most recent Message event (the unary result when no
	// task ever came into existence).
	lastMessage *protocol.Message
	// lastStatusMsg is the most recent superseded status message moved into the
	// conversation, compared by identity, so a message reused across several
	// status updates in this round is not appended to the history more than once.
	lastStatusMsg *protocol.Message
	// finalTask is the task snapshot taken after the close rules ran; it is
	// safe to read once done is closed.
	finalTask *protocol.Task
	// runErr is the first asynchronous event-persistence failure. The engine
	// drains the processor channel after it is set; result callers are notified
	// immediately through runFailed and may also read it after done closes.
	runErr error
	// runFailed carries runErr without waiting for the MessageProcessor to close
	// its channel. It is buffered so the draining engine never waits for a caller.
	runFailed chan error
	// taskTouched records whether THIS round wrote to the task (lazy create,
	// status/artifact persist, violation or close-rule write). §3.1 keys the
	// unary result on it: a continuation round that only emits Messages
	// answers with the last Message, not the untouched task snapshot.
	taskTouched bool
	// inlinePushPending defers a fresh task's inline config until the first task
	// event has been persisted, avoiding orphan configs for pure-Message rounds.
	inlinePushPending bool
	// yielded records that this round emitted a suspend state (§3.4) and gave
	// the task up: a continuation may already own it, so later events from
	// this round are discarded and the close rules are skipped.
	yielded bool

	// pipe is the message/stream request pipe; nil for message/send.
	pipe *taskSubscriber
	// historyLength shapes only the operation-local Task framing sent on pipe.
	// It is a private copy so callers may safely reuse or mutate their request.
	historyLength *int

	// immediateResult carries the immediate result (first persisted task
	// snapshot or first Message) for returnImmediately=true. Buffered so the
	// engine never blocks on it.
	immediateResult     chan sendOutcome
	immediateResultSent bool

	// done is closed after the engine ran the close rules, closed the pipe,
	// and deregistered; the blocking unary waiter reads results after it.
	done chan struct{}
}

// resolveContinuation loads and validates the task a continuation message
// targets, returning the stored snapshot. A message without a taskId is a
// fresh round and resolves to nil. When taskId is set it is the same value
// the caller registered the execution under (see prepareExecution).
func (m *TaskManager) resolveContinuation(
	ctx context.Context,
	tenant string,
	owner string,
	message *protocol.Message,
) (*protocol.Task, error) {
	if message.TaskID == nil || *message.TaskID == "" {
		return nil, nil
	}
	taskID := *message.TaskID

	loaded, err := m.getTaskInternal(ctx, tenant, owner, taskID)
	if err != nil {
		return nil, err
	}
	if isFinalState(loaded.Status.State) {
		// A terminal task is immutable: reject without invoking the MessageProcessor.
		return nil, taskmanager.ErrInvalidParams(
			fmt.Sprintf("task %s is in terminal state %s", loaded.ID, loaded.Status.State))
	}
	if message.ContextID != nil && *message.ContextID != "" && *message.ContextID != loaded.ContextID {
		// A continuation must stay in the task's own conversation: a foreign
		// contextId would resolve the wrong ec.History and contradict the
		// task snapshot's ContextID.
		return nil, taskmanager.ErrInvalidParams(
			fmt.Sprintf("message contextId does not match task %s context", loaded.ID))
	}
	return loaded, nil
}

// prepareExecution runs the shared message/send + message/stream request
// preparation: it stores the request message, resolves continuation vs new
// task ID, builds the ExecContext, registers the cancel handle, and invokes
// the MessageProcessor. On success the engine goroutine owns the event channel.
//
//nolint:gocyclo // Preparation keeps admission, continuation lease, history, and processor handoff in one rollback path.
func (m *TaskManager) prepareExecution(
	ctx context.Context,
	request *protocol.SendMessageParams,
	streaming bool,
) (*execution, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	message := &request.Message
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}

	// ec.TaskID is always pre-allocated; whether a task comes into existence
	// is up to the MessageProcessor (lazy creation).
	var taskID string
	if message.TaskID != nil && *message.TaskID != "" {
		taskID = *message.TaskID
	} else {
		taskID = protocol.GenerateTaskID()
	}

	// The MessageProcessor context is detached from the request context (client
	// disconnect must not cancel the work) but cancelable via OnCancelTask.
	execCtx, cancel := context.WithCancel(detachedCtx{parent: ctx})
	ex := &execution{
		manager:         m,
		ec:              &taskmanager.ExecContext{},
		live:            &liveExecution{cancel: cancel},
		owner:           owner,
		immediateResult: make(chan sendOutcome, 1),
		runFailed:       make(chan error, 1),
		done:            make(chan struct{}),
	}
	if streaming {
		// Reserve one framing slot for the synthetic initial Task. The configured
		// capacity remains available to processor updates, including when the
		// request pipe uses non-blocking sends.
		ex.pipe = newTaskSubscriber(taskID, m.options.TaskSubscriberBufSize+1,
			m.options.TaskSubscriberBlockingSend)
		ex.live.pipe = ex.pipe
	}

	// Register-or-reject atomically under the registry lock before the
	// continuation load, the message store, and the MessageProcessor: a task admits
	// at most one live run, and a cancel arriving mid-round must find the
	// handle. Registering before the load also orders the load after the
	// previous round's (or a no-live cancel's) last write — the slot frees
	// only after that write — so the working copy below can never be a stale
	// pre-terminal snapshot that would smuggle writes past a terminal state.
	// A rejected request never reaches the MessageProcessor and leaves no trace.
	if err := m.registerExecution(ctx, request.Tenant, owner, taskID, ex.live); err != nil {
		cancel()
		return nil, err
	}

	// Continuation: a request addressing an existing task loads it, rejects
	// terminal tasks without invoking the MessageProcessor (the task is frozen), and
	// hands the MessageProcessor the current snapshot via ec.Task.
	task, err := m.resolveContinuation(ctx, request.Tenant, owner, message)
	if err != nil {
		m.releaseExecution(request.Tenant, owner, taskID, ex.live)
		cancel()
		return nil, err
	}
	if task != nil {
		// A continuation may take ownership just before the old TTL elapses.
		// Renew it synchronously before invoking user code; the engine ticker
		// takes over once ProcessMessage returns its channel.
		if err := m.refreshTaskLease(ctx, request.Tenant, owner, taskID); err != nil {
			m.releaseExecution(request.Tenant, owner, taskID, ex.live)
			cancel()
			return nil, err
		}
		// A follow-up without an explicit contextId continues the task's
		// conversation; otherwise ec.History would miss the earlier turns.
		if (message.ContextID == nil || *message.ContextID == "") && task.ContextID != "" {
			contextID := task.ContextID
			message.ContextID = &contextID
		}
	}
	acceptedOutputModes, inlinePushConfig := messageConfigurationValues(request.Configuration)
	pushConfig, pending, err := m.prepareInlinePushConfig(request.Tenant, owner, taskID, task, inlinePushConfig)
	if err != nil {
		m.releaseExecution(request.Tenant, owner, taskID, ex.live)
		cancel()
		return nil, err
	}

	// Store the user message into the conversation, stamping ContextID when
	// absent.
	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}
	contextID := *message.ContextID
	if task != nil {
		// A follow-up supersedes the task's current status message. Move it into
		// history before the new user turn and clear it from Status, matching the
		// reference SDKs and avoiding the same message in both Task fields.
		if task.Status.Message != nil {
			statusMessage := task.Status.Message
			task.Status.Message = nil
			// Clearing the current status message changes the Task snapshot. Commit a
			// same-state status event with it so a cross-node subscriber that loaded
			// the old snapshot cannot miss this continuation-side transition.
			event := &protocol.TaskStatusUpdateEvent{
				TaskID:    task.ID,
				ContextID: task.ContextID,
				Status:    task.Status,
			}
			if err := m.commitTaskEvent(ctx, request.Tenant, owner, task, protocol.NewStreamResponseStatusUpdate(event), false); err != nil {
				m.releaseExecution(request.Tenant, owner, taskID, ex.live)
				cancel()
				return nil, fmt.Errorf("failed to advance task status history: %w", err)
			}
			m.storeStatusMessage(request.Tenant, owner, taskID, contextID, statusMessage)
		}
		// The engine's working copy must not alias ec.Task (the MessageProcessor's
		// read-only snapshot).
		ex.task = copyTask(task)
	}
	m.storeMessage(context.Background(), request.Tenant, owner, *message)

	// History is the conversation snapshot truncated per the manager
	// configuration; the request's historyLength only shapes response tasks.
	history, err := m.getConversationHistory(ctx, request.Tenant, owner, contextID, m.options.MaxHistoryLength)
	if err != nil {
		log.Warnf("RedisTaskManager: failed to load history for context %s: %v", contextID, err)
	}
	*ex.ec = taskmanager.ExecContext{
		TaskID:              taskID,
		Task:                task,
		Message:             *message,
		ContextID:           contextID,
		Tenant:              request.Tenant,
		History:             history,
		AcceptedOutputModes: acceptedOutputModes,
		PushConfig:          pushConfig,
	}
	ex.inlinePushPending = pending
	if requested := historyLengthFromConfig(request.Configuration); requested != nil {
		value := *requested
		ex.historyLength = &value
	}

	processorEC := *ex.ec
	if ex.ec.PushConfig != nil {
		pushConfig := *ex.ec.PushConfig
		if pushConfig.Authentication != nil {
			auth := *pushConfig.Authentication
			pushConfig.Authentication = &auth
		}
		processorEC.PushConfig = &pushConfig
	}
	events, err := m.processor.ProcessMessage(execCtx, &processorEC)
	if err != nil {
		m.releaseExecution(request.Tenant, owner, taskID, ex.live)
		cancel()
		return nil, err
	}
	if events == nil {
		m.releaseExecution(request.Tenant, owner, taskID, ex.live)
		cancel()
		return nil, taskmanager.ErrInternalError("processor returned nil channel")
	}

	go func() {
		defer m.engineWg.Done()
		ex.run(events)
	}()
	return ex, nil
}

func messageConfigurationValues(
	config *protocol.SendMessageConfiguration,
) ([]string, *protocol.TaskPushNotificationConfig) {
	if config == nil {
		return nil, nil
	}
	return config.AcceptedOutputModes, config.PushConfig
}

// prepareInlinePushConfig validates and snapshots an inline registration.
// Existing tasks persist it immediately; fresh lazy tasks wait for their first
// task event so a failed start or pure-Message reply cannot leave an orphan.
func (m *TaskManager) prepareInlinePushConfig(
	tenant string,
	owner string,
	taskID string,
	task *protocol.Task,
	config *protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, bool, error) {
	if config == nil {
		return nil, false, nil
	}
	if !m.pushEnabled {
		return nil, false, taskmanager.ErrPushNotificationNotSupported()
	}
	pushConfig := *config
	pushConfig.Tenant = tenant
	pushConfig.TaskID = taskID
	if pushConfig.ID == "" {
		pushConfig.ID = taskID
	}
	if err := push.ValidateConfig(pushConfig); err != nil {
		return nil, false, taskmanager.ErrInvalidParams(err.Error())
	}
	if task != nil {
		if _, err := m.storePushConfig(context.Background(), owner, pushConfig); err != nil {
			return nil, false, err
		}
		return &pushConfig, false, nil
	}
	return &pushConfig, true, nil
}

// run is the engine loop. It consumes events in order, persisting each task
// event before broadcasting it, and always drains the channel to closure so
// the MessageProcessor's sends never leak.
func (ex *execution) run(events <-chan protocol.StreamEvent) {
	leaseInterval := ex.manager.expiration / 3
	if leaseInterval < time.Millisecond {
		leaseInterval = time.Millisecond
	}
	leaseTicker := time.NewTicker(leaseInterval)
	defer leaseTicker.Stop()

	mode := engineConsuming
	for {
		select {
		case event, ok := <-events:
			if !ok {
				ex.finish()
				return
			}
			if ex.runErr != nil {
				log.Warnf("RedisTaskManager: discarding %T for task %s after execution persistence failed",
					event, ex.ec.TaskID)
				continue
			}
			switch mode {
			case engineDrainTerminal:
				log.Warnf("RedisTaskManager: discarding %T for task %s emitted after terminal state",
					event, ex.ec.TaskID)
				continue
			case engineDrainViolation:
				log.Warnf("RedisTaskManager: discarding %T for task %s emitted after a contract violation",
					event, ex.ec.TaskID)
				continue
			case engineDrainYielded:
				log.Warnf("RedisTaskManager: discarding %T for task %s emitted after the round yielded (suspended task)",
					event, ex.ec.TaskID)
				continue
			case engineDrainMessage:
				log.Warnf("RedisTaskManager: discarding %T for task %s emitted after a direct Message response",
					event, ex.ec.TaskID)
				continue
			}
			mode = ex.handleEvent(event)
		case <-leaseTicker.C:
			ex.refreshTaskLease()
		}
	}
}

func (ex *execution) refreshTaskLease() {
	if ex.task == nil || isFinalState(ex.task.Status.State) || ex.yielded || ex.runErr != nil {
		return
	}
	err := ex.manager.refreshTaskLease(context.Background(), ex.ec.Tenant, ex.owner, ex.ec.TaskID)
	if err == nil {
		return
	}
	if errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		ex.failRun(fmt.Errorf("failed to renew live task %s: %w", ex.ec.TaskID, err))
		return
	}
	log.Warnf("RedisTaskManager: failed to renew live task %s: %v", ex.ec.TaskID, err)
}

// failRun exposes a persistence failure immediately while the engine keeps
// draining the processor channel in the background. Canceling the processor
// context asks well-behaved producers to close promptly; closing the request
// pipe prevents a streaming caller from waiting on a stream that cannot commit
// any more events.
func (ex *execution) failRun(err error) {
	if err == nil || ex.runErr != nil {
		return
	}
	ex.runErr = err
	ex.runFailed <- err
	ex.live.cancel()
	ex.closePipe()
	log.Errorf("RedisTaskManager: %v", err)
}

// handleEvent processes one event and returns the next consume mode.
func (ex *execution) handleEvent(event protocol.StreamEvent) int {
	switch ev := event.(type) {
	case *protocol.Task:
		// Task snapshots are materialized by the framework from the event
		// stream; an MessageProcessor emitting one is a contract violation.
		ex.violate("processor emitted forbidden Task snapshot")
		return engineDrainViolation
	case *protocol.Message:
		if ev == nil {
			log.Warnf("RedisTaskManager: ignoring nil Message event for task %s", ex.ec.TaskID)
			return engineConsuming
		}
		if !ex.checkMessageIDs(ev) {
			ex.violate("processor emitted event for foreign task")
			return engineDrainViolation
		}
		direct := !ex.taskTouched
		ex.processMessageEvent(ev)
		if ex.runErr != nil {
			return engineConsuming
		}
		// The first valid event selects the response shape. Once a round returns
		// a direct Message, end its wire response and only drain later events.
		if direct {
			ex.closePipe()
			return engineDrainMessage
		}
		return engineConsuming
	case *protocol.TaskStatusUpdateEvent:
		if ev == nil {
			log.Warnf("RedisTaskManager: ignoring nil status event for task %s", ex.ec.TaskID)
			return engineConsuming
		}
		if !ex.stampTaskEventIDs(&ev.TaskID, &ev.ContextID) {
			ex.violate("processor emitted event for foreign task")
			return engineDrainViolation
		}
		ex.processStatusEvent(ev)
		if ex.runErr != nil {
			return engineConsuming
		}
		if ex.task != nil && isFinalState(ex.task.Status.State) {
			return engineDrainTerminal
		}
		if ex.yielded {
			return engineDrainYielded
		}
		return engineConsuming
	case *protocol.TaskArtifactUpdateEvent:
		if ev == nil {
			log.Warnf("RedisTaskManager: ignoring nil artifact event for task %s", ex.ec.TaskID)
			return engineConsuming
		}
		if !ex.stampTaskEventIDs(&ev.TaskID, &ev.ContextID) {
			ex.violate("processor emitted event for foreign task")
			return engineDrainViolation
		}
		ex.processArtifactEvent(ev)
		return engineConsuming
	default:
		// StreamEvent is sealed; an unknown type cannot drive any task.
		log.Warnf("RedisTaskManager: ignoring unknown event type %T for task %s", event, ex.ec.TaskID)
		return engineConsuming
	}
}

// finish applies the channel-close rules, closes the pipe, deregisters the
// execution, and signals the unary waiter. A yielded round (§3.4) skips the
// close rules: ownership of the task moved on at the suspend event — a
// continuation or a no-live cancel may already be writing it.
func (ex *execution) finish() {
	if ex.runErr == nil && ex.task != nil && !ex.yielded && !isFinalState(ex.task.Status.State) {
		switch {
		case ex.live.cancelRequested.Load():
			// Cancellation-triggered close: the framework marks the task
			// CANCELED on the MessageProcessor's behalf. If the MessageProcessor emitted its
			// own terminal state after the cancel, it won (handled above:
			// the task is already terminal and this branch is skipped).
			ex.processStatusEvent(&protocol.TaskStatusUpdateEvent{
				Status: protocol.TaskStatus{State: protocol.TaskStateCanceled},
			})
		case !ex.taskTouched:
			// This round never wrote to the task: it must not apply close rules
			// to state some other round left behind (the close rules are
			// round-scoped).
		case ex.task.Status.State == protocol.TaskStateSubmitted ||
			ex.task.Status.State == protocol.TaskStateWorking:
			// Finishing without a conclusion is an MessageProcessor bug: fail
			// explicitly rather than pretend completion.
			ex.failTask("processor finished without terminal state")
		default:
			// input-required / auth-required: the task stays suspended
			// awaiting a follow-up message (continuation round).
		}
	}
	// §3.1: only rounds that wrote to the task answer with a Task snapshot; a
	// continuation that merely replied with Messages answers with the Message.
	if ex.runErr == nil && ex.task != nil && ex.taskTouched {
		ex.finalTask = copyTask(ex.task)
	}
	if ex.pipe != nil {
		ex.pipe.Close()
	}
	ex.manager.deregisterExecution(ex.ec.Tenant, ex.owner, ex.ec.TaskID, ex.live)
	ex.live.cancel()
	close(ex.done)
}

// violate logs a contract violation and, when a non-terminal task exists,
// persists and broadcasts FAILED so the violation is observable.
func (ex *execution) violate(reason string) {
	log.Errorf("RedisTaskManager: processor contract violation for task %s: %s", ex.ec.TaskID, reason)
	if ex.task != nil && !isFinalState(ex.task.Status.State) {
		ex.failTask(reason)
	}
}

// failTask persists and broadcasts a FAILED status carrying the reason as the
// status message.
func (ex *execution) failTask(reason string) {
	ex.processStatusEvent(&protocol.TaskStatusUpdateEvent{
		Status: protocol.TaskStatus{
			State:   protocol.TaskStateFailed,
			Message: ex.failureStatusMessage(reason),
		},
	})
}

// failureStatusMessage wraps a framework failure reason as an agent message
// addressed to this execution's task.
func (ex *execution) failureStatusMessage(text string) *protocol.Message {
	message := protocol.NewMessage(protocol.MessageRoleAgent, []*protocol.Part{protocol.NewTextPart(text)})
	contextID := ex.ec.ContextID
	taskID := ex.ec.TaskID
	message.ContextID = &contextID
	message.TaskID = &taskID
	return &message
}

// checkMessageIDs validates a Message event against the ExecContext: an
// MessageProcessor may leave IDs empty (the framework stamps them) but must not
// address a foreign task or context.
func (ex *execution) checkMessageIDs(msg *protocol.Message) bool {
	if msg.TaskID != nil && *msg.TaskID != "" && *msg.TaskID != ex.ec.TaskID {
		return false
	}
	if msg.ContextID != nil && *msg.ContextID != "" && *msg.ContextID != ex.ec.ContextID {
		return false
	}
	return true
}

// stampTaskEventIDs fills empty TaskID/ContextID with the ExecContext's and
// reports false on a mismatch: one Execute may only drive its own task.
func (ex *execution) stampTaskEventIDs(taskID, contextID *string) bool {
	switch *taskID {
	case "":
		*taskID = ex.ec.TaskID
	case ex.ec.TaskID:
	default:
		return false
	}
	switch *contextID {
	case "":
		*contextID = ex.ec.ContextID
	case ex.ec.ContextID:
	default:
		return false
	}
	return true
}

// processMessageEvent stores a reply Message into the conversation and
// forwards it to the request pipe. When a task exists, the event journal was
// already updated by appendTaskEvent.
// The immediateResult outcome is offered before response delivery so a
// returnImmediately waiter is never stalled behind a response pipe or push.
func (ex *execution) processMessageEvent(msg *protocol.Message) {
	contextID := ex.ec.ContextID
	ex.manager.stampReplyMessage(&contextID, msg)
	response := protocol.NewStreamResponseMessage(msg)
	if ex.task != nil {
		if err := ex.manager.appendTaskEvent(context.Background(), ex.ec.Tenant, ex.owner, ex.ec.TaskID, response); err != nil {
			ex.failRun(fmt.Errorf("failed to store message event for task %s: %w", ex.ec.TaskID, err))
			return
		}
	}
	// The event journal is the success boundary for a reply attached to a Task.
	// Store conversation history only after the append succeeds, so a failed
	// request cannot leave behind an agent reply that no stream observed.
	ex.manager.storeMessage(context.Background(), ex.ec.Tenant, ex.owner, *msg)
	ex.lastMessage = msg
	ex.offerImmediateResult(sendOutcome{message: msg})
	ex.broadcast(response)
}

// storeStatusMessage stores a stamped copy without mutating the processor's
// message, which may still be visible in an earlier task snapshot or wire event.
func (m *TaskManager) storeStatusMessage(tenant, owner, taskID, contextID string, message *protocol.Message) {
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
	m.storeMessage(context.Background(), tenant, owner, stored)
}

// rollStatusMessage moves a superseded status message into conversation
// history. The current status message stays on Task.Status until the next
// status transition or follow-up user message, matching the reference SDKs.
// Pointer checks avoid redundant work within a round; storeMessage provides the
// final MessageID-based idempotency guard.
func (ex *execution) rollStatusMessage(message *protocol.Message) {
	if message == nil || message == ex.lastStatusMsg || message == ex.lastMessage {
		return
	}
	ex.lastStatusMsg = message
	ex.manager.storeStatusMessage(ex.ec.Tenant, ex.owner, ex.ec.TaskID, ex.ec.ContextID, message)
}

// processStatusEvent applies a status update to the task (lazily creating it
// on the first task event), persists it, and only then broadcasts it: at any
// moment GetTask reads a state >= what the stream has delivered.
func (ex *execution) processStatusEvent(ev *protocol.TaskStatusUpdateEvent) {
	if ev.Status.State == "" || ev.Status.State == protocol.TaskStateUnspecified {
		// A stateless status event would strand the task in a dangling
		// non-terminal state (never TTL-collected, resubscribable forever).
		ex.violate("status event without a state")
		return
	}
	// A terminal state is immutable: never write over one, whoever set it.
	if ex.task != nil && isFinalState(ex.task.Status.State) {
		log.Warnf("RedisTaskManager: discarding status %s for task %s already in terminal state %s",
			ev.Status.State, ex.ec.TaskID, ex.task.Status.State)
		return
	}
	// Internal callers (close rules, violations) pass events with empty IDs.
	ex.stampTaskEventIDs(&ev.TaskID, &ev.ContextID)
	timestamp := ev.Status.Timestamp
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	status := protocol.TaskStatus{
		State:     ev.Status.State,
		Message:   ev.Status.Message,
		Timestamp: timestamp,
	}
	var initial *protocol.Task
	if !ex.taskTouched {
		if ex.task == nil {
			initial = protocol.NewTask(ex.ec.TaskID, ex.ec.ContextID)
		} else {
			initial = copyTask(ex.task)
		}
	}
	var previousStatusMessage *protocol.Message
	allowCreate := ex.task == nil
	if allowCreate {
		ex.task = ex.newTask(ev.TaskID, ev.ContextID, status)
	} else {
		previousStatusMessage = ex.task.Status.Message
		ex.task.Status = status
	}
	ex.taskTouched = true
	ev.Status = status
	final := isFinalState(status.State)
	suspended := !final && isSuspendedState(status.State)
	ev.Final = final
	yielding := false
	if suspended {
		// Establish the handoff before the suspended state can become visible in
		// Redis. A polling client may otherwise observe INPUT_REQUIRED and still
		// be rejected as a concurrent execution. If cancellation claimed the slot
		// first, keep this round active so its close rule can persist CANCELED
		// rather than swallowing the accepted cancellation.
		yielding = ex.manager.beginExecutionYield(ex.ec.Tenant, ex.owner, ex.ec.TaskID, ex.live)
	}
	// Persist before broadcast (consistency order). A failure terminates the
	// execution result: the mutated working copy is never returned as if it had
	// committed successfully.
	response := protocol.NewStreamResponseStatusUpdate(ev)
	if err := ex.manager.commitTaskEvent(context.Background(), ex.ec.Tenant, ex.owner, ex.task, response, allowCreate); err != nil {
		if yielding {
			ex.manager.abortExecutionYield(ex.ec.Tenant, ex.owner, ex.ec.TaskID, ex.live)
		}
		ex.failRun(fmt.Errorf("failed to store task %s status %s: %w", ev.TaskID, status.State, err))
		return
	}
	ex.sendInitialTask(initial)
	// The old status message is durably superseded only after the Task/event
	// commit succeeds. Moving it earlier would leave failed updates reflected in
	// conversation history while the stored Task still carried the same message.
	ex.rollStatusMessage(previousStatusMessage)
	if err := ex.persistInlinePushConfig(); err != nil {
		if yielding {
			ex.manager.abortExecutionYield(ex.ec.Tenant, ex.owner, ex.ec.TaskID, ex.live)
		}
		log.Errorf("RedisTaskManager: failed to persist inline push config for task %s: %v", ex.ec.TaskID, err)
		ex.failTask("failed to persist inline push config")
		return
	}
	if yielding {
		ex.yielded = true
		// Queue automatic push before exposing the suspend state. A full bounded
		// queue may delay publication, but a client can never observe a state it
		// cannot yet continue. The handoff starts before enqueue so a client that
		// polls the persisted task also waits instead of being rejected. It also
		// serializes current-request delivery with a continuation round.
		ex.manager.dispatchPush(ex.ec.Tenant, ex.owner, ex.ec.TaskID, response)
		ex.offerImmediateTask()
		ex.broadcastWithoutPush(response)
		ex.closePipe()
		// The suspended round no longer owns the task after publishing its
		// final frame. Cancel its processor context before removing it from the
		// registry: Close cannot discover the execution once the slot is released,
		// but still waits for its drain engine to finish.
		ex.live.cancel()
		ex.manager.deregisterExecution(ex.ec.Tenant, ex.owner, ex.ec.TaskID, ex.live)
		return
	}

	// Immediate result first: a returnImmediately waiter must never be stalled
	// behind response delivery or push dispatch below.
	ex.offerImmediateTask()
	ex.broadcast(response)
	if final {
		// Nothing can follow a terminal frame: end the response stream here
		// instead of trusting the MessageProcessor to close its channel promptly.
		ex.closePipe()
	}
}

// processArtifactEvent appends the artifact to the task (lazily creating it
// in submitted state if this is the first task event), persists, then
// broadcasts.
func (ex *execution) processArtifactEvent(ev *protocol.TaskArtifactUpdateEvent) {
	// A terminal state is immutable: no artifact may land after one.
	if ex.task != nil && isFinalState(ex.task.Status.State) {
		log.Warnf("RedisTaskManager: discarding artifact for task %s already in terminal state %s",
			ex.ec.TaskID, ex.task.Status.State)
		return
	}
	var initial *protocol.Task
	if !ex.taskTouched {
		if ex.task == nil {
			initial = protocol.NewTask(ex.ec.TaskID, ex.ec.ContextID)
		} else {
			initial = copyTask(ex.task)
		}
	}
	allowCreate := ex.task == nil
	if allowCreate {
		ex.task = ex.newTask(ev.TaskID, ev.ContextID, protocol.TaskStatus{
			State:     protocol.TaskStateSubmitted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}
	var appendedAsNew bool
	ex.task.Artifacts, appendedAsNew = protocol.AppendArtifact(ex.task.Artifacts, ev.Artifact, ev.Append != nil && *ev.Append)
	if appendedAsNew {
		log.Warnf("RedisTaskManager: artifact %s for task %s used append=true with no prior chunk; stored as a new artifact",
			ev.Artifact.ArtifactID, ex.ec.TaskID)
	}
	ex.taskTouched = true
	// Persist before broadcast (consistency order).
	response := protocol.NewStreamResponseArtifactUpdate(ev)
	if err := ex.manager.commitTaskEvent(context.Background(), ex.ec.Tenant, ex.owner, ex.task, response, allowCreate); err != nil {
		ex.failRun(fmt.Errorf("failed to store task %s artifact: %w", ev.TaskID, err))
		return
	}
	ex.sendInitialTask(initial)
	if err := ex.persistInlinePushConfig(); err != nil {
		log.Errorf("RedisTaskManager: failed to persist inline push config for task %s: %v", ex.ec.TaskID, err)
		ex.failTask("failed to persist inline push config")
		return
	}
	// Immediate result first: a returnImmediately waiter must never be stalled
	// behind response delivery or push dispatch below.
	ex.offerImmediateTask()
	ex.broadcast(response)
}

func (ex *execution) persistInlinePushConfig() error {
	if !ex.inlinePushPending {
		return nil
	}
	ex.inlinePushPending = false
	if _, err := ex.manager.storePushConfig(context.Background(), ex.owner, *ex.ec.PushConfig); err != nil {
		return err
	}
	return nil
}

// newTask lazily materializes the task on the first task event, seeding the
// same empty collections the previous task builder used.
func (ex *execution) newTask(taskID, contextID string, status protocol.TaskStatus) *protocol.Task {
	return &protocol.Task{
		ID:        taskID,
		ContextID: contextID,
		Status:    status,
		Artifacts: make([]protocol.Artifact, 0),
		History:   make([]protocol.Message, 0),
		Metadata:  make(map[string]interface{}),
	}
}

// closePipe ends the message/stream response stream, if any. Subscriber close
// is CAS-guarded, so calling it from more than one place is safe.
func (ex *execution) closePipe() {
	if ex.pipe != nil {
		ex.pipe.Close()
	}
}

// sendInitialTask shapes and sends the operation-local pre-update Task frame.
// The snapshot is private to this response, so filling History does not mutate
// the stored Task or create another Redis journal event.
func (ex *execution) sendInitialTask(task *protocol.Task) {
	if task == nil || ex.pipe == nil {
		return
	}
	ex.manager.fillTaskHistory(context.Background(), ex.ec.Tenant, ex.owner, task, ex.historyLength)
	if err := ex.pipe.Send(protocol.NewStreamResponseTask(task)); err != nil {
		log.Warnf("RedisTaskManager: failed to send initial Task for task %s: %v", ex.ec.TaskID, err)
	}
}

// broadcast forwards an event to the request pipe and, when a task exists,
// dispatches push notifications. The event journal was already updated by the
// caller and SubscribeToTask readers consume it independently.
func (ex *execution) broadcast(event protocol.StreamResponse) {
	if ex.pipe != nil {
		if err := ex.pipe.Send(event); err != nil {
			log.Warnf("RedisTaskManager: failed to send event to request pipe for task %s: %v", ex.ec.TaskID, err)
		}
	}
	if ex.task != nil {
		ex.manager.dispatchPush(ex.ec.Tenant, ex.owner, ex.ec.TaskID, event)
	}
}

// broadcastWithoutPush publishes an event whose automatic push delivery was
// already enqueued by the caller.
func (ex *execution) broadcastWithoutPush(event protocol.StreamResponse) {
	if ex.pipe != nil {
		if err := ex.pipe.Send(event); err != nil {
			log.Warnf("RedisTaskManager: failed to send event to request pipe for task %s: %v", ex.ec.TaskID, err)
		}
	}
}

// offerImmediateResult publishes the immediate result exactly once (for
// returnImmediately=true waiters).
func (ex *execution) offerImmediateResult(out sendOutcome) {
	if ex.immediateResultSent {
		return
	}
	ex.immediateResultSent = true
	ex.immediateResult <- out // buffered, single write: never blocks
}

// offerImmediateTask publishes the just-persisted task snapshot as the immediateResult
// event.
func (ex *execution) offerImmediateTask() {
	if ex.immediateResultSent {
		return
	}
	ex.offerImmediateResult(sendOutcome{task: copyTask(ex.task)})
}

// copyTask returns a copy safe to hand out of the engine.
func copyTask(task *protocol.Task) *protocol.Task {
	snapshot := *task
	snapshot.Artifacts = append([]protocol.Artifact(nil), task.Artifacts...)
	snapshot.History = append([]protocol.Message(nil), task.History...)
	return &snapshot
}
