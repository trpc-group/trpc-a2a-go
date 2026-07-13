// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package memory provides an in-memory TaskManager driven by the
// taskmanager.MessageProcessor event contract: the framework owns the task lifecycle
// (lazy creation, persistence, subscriber fan-out) and derives every response
// from the event stream returned by the MessageProcessor.
package memory

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const defaultMaxHistoryLength = 100
const defaultCleanupInterval = 30 * time.Second
const defaultConversationTTL = 1 * time.Hour
const defaultTaskTTL = 0
const defaultSubscriberBufferSize = 1024

// unlimitedHistoryLength is used to request the full conversation history when
// a v1.0 request leaves historyLength unset (spec: unset means no limit).
const unlimitedHistoryLength = 1 << 30

// ConversationHistory stores conversation history information
type ConversationHistory struct {
	// MessageIDs is the list of message IDs, ordered by time
	MessageIDs []string
	// LastAccessTime is the last access time
	LastAccessTime time.Time
}

// taskSubscriber is one fan-out endpoint of a task's event stream: the
// message/stream request pipe or a tasks/resubscribe subscription. Events are
// queued on a buffered channel; with blockingSend the producer waits for a
// free slot, otherwise a full queue rejects the event with an error.
type taskSubscriber struct {
	taskID     string
	eventQueue chan protocol.StreamResponse
	done       chan struct{}
	closed     atomic.Bool
	// mu serializes Send against Close so an event is never sent on a closed channel.
	mu           sync.RWMutex
	blockingSend bool
}

// newTaskSubscriber creates a subscriber with the given buffer size
// (a non-positive size falls back to the default).
func newTaskSubscriber(taskID string, bufSize int, blockingSend bool) *taskSubscriber {
	if bufSize <= 0 {
		bufSize = defaultSubscriberBufferSize
	}
	return &taskSubscriber{
		taskID:       taskID,
		eventQueue:   make(chan protocol.StreamResponse, bufSize),
		done:         make(chan struct{}),
		blockingSend: blockingSend,
	}
}

// Close closes the subscriber. It is safe to call multiple times and unblocks
// any in-flight blocking Send (done is closed before the write lock is taken).
func (s *taskSubscriber) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	close(s.done)

	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.eventQueue)
}

// Channel returns the receive side of the subscriber.
func (s *taskSubscriber) Channel() <-chan protocol.StreamResponse {
	return s.eventQueue
}

// Closed reports whether the subscriber is closed.
func (s *taskSubscriber) Closed() bool {
	return s.closed.Load()
}

// Send delivers one event to the subscriber.
func (s *taskSubscriber) Send(event protocol.StreamResponse) error {
	if s.Closed() {
		return fmt.Errorf("task subscriber is closed")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Closed() {
		return fmt.Errorf("task subscriber is closed")
	}

	if s.blockingSend {
		select {
		case s.eventQueue <- event:
			return nil
		case <-s.done:
			return fmt.Errorf("task subscriber is closed")
		}
	}

	select {
	case s.eventQueue <- event:
		return nil
	case <-s.done:
		return fmt.Errorf("task subscriber is closed")
	default:
		return fmt.Errorf("event queue is full or closed")
	}
}

// TaskManager is the in-memory implementation of the taskmanager.TaskManager
// interface. Agent behavior is delegated to the injected MessageProcessor; the manager
// materializes tasks from the MessageProcessor's event stream.
type TaskManager struct {
	// processor is the user-provided agent logic invoked for every incoming message.
	processor taskmanager.MessageProcessor

	// messages stores all Messages, indexed by messageID
	// key: messageID, value: Message
	messages map[string]protocol.Message

	// conversations stores the message history of each conversation, indexed by contextID
	// key: contextID, value: ConversationHistory
	conversations map[string]*ConversationHistory

	// conversationMu protects the messages and conversations fields
	conversationMu sync.RWMutex

	// tasks stores the task snapshots, indexed by taskID. The drain engine is
	// the writer while an execution is live; OnCancelTask writes only when no
	// execution is live (see OnCancelTask).
	tasks map[string]*protocol.Task

	// taskMu protects the tasks and subscribers fields
	taskMu sync.RWMutex

	// subscribers stores the fan-out endpoints of each task's event stream
	// key: taskID, value: subscriber list
	subscribers map[string][]*taskSubscriber

	// pushStore persists push-notification configs (multiple per task, addressed
	// by config ID). It is an internal detail; pluggable storage backends are
	// not part of the public API yet.
	pushStore *pushConfigStore

	// pushSender, when non-nil, delivers task updates to registered webhooks as
	// events occur. When nil, push is not supported: the config RPCs return
	// PushNotificationNotSupported.
	pushSender push.Sender

	// pushWg tracks in-flight asynchronous push deliveries so Close can drain them.
	pushWg sync.WaitGroup
	// pushCtx bounds every push delivery; pushCancel is called in Close so a slow
	// or hung webhook cannot stall shutdown — the in-flight send is cancelled
	// rather than awaited to its full timeout.
	pushCtx    context.Context
	pushCancel context.CancelFunc

	// executions tracks the cancellation handle of every live MessageProcessor run,
	// keyed by task ID. Registered before ProcessMessage, removed when the engine
	// finishes; OnCancelTask cancels through it.
	executions map[string]*execution
	// execMu protects the executions and closed fields
	execMu sync.Mutex
	// closed rejects new runs once Close has begun tearing the manager down.
	closed bool
	// engineWg counts live drain engines so Close can wait for their final
	// persists instead of clearing state under them.
	engineWg sync.WaitGroup

	// options
	options *TaskManagerOptions

	// stopCleanup signals the cleanup goroutine to stop
	stopCleanup chan struct{}
	// cleanupWg tracks the cleanup goroutine so Close can wait for it to exit
	cleanupWg sync.WaitGroup
	closeOnce sync.Once
}

var _ taskmanager.TaskManager = (*TaskManager)(nil)

// NewTaskManager creates a new TaskManager instance driven by the given MessageProcessor.
func NewTaskManager(processor taskmanager.MessageProcessor, opts ...TaskManagerOption) (*TaskManager, error) {
	if processor == nil {
		return nil, fmt.Errorf("processor cannot be nil")
	}

	// Apply default options
	options := DefaultTaskManagerOptions()

	// Apply user options
	for _, opt := range opts {
		opt(options)
	}
	if options.Push.ManualDelivery && options.Push.Sender == nil {
		return nil, fmt.Errorf("push.Config.ManualDelivery requires a Sender")
	}

	manager := &TaskManager{
		processor:     processor,
		messages:      make(map[string]protocol.Message),
		conversations: make(map[string]*ConversationHistory),
		tasks:         make(map[string]*protocol.Task),
		subscribers:   make(map[string][]*taskSubscriber),
		pushStore:     newPushConfigStore(),
		pushSender:    options.Push.Sender,
		executions:    make(map[string]*execution),
		options:       options,
		stopCleanup:   make(chan struct{}),
	}
	manager.pushCtx, manager.pushCancel = context.WithCancel(context.Background())

	// Start cleanup goroutine if enabled
	if options.EnableCleanup {
		manager.cleanupWg.Add(1)
		go func() {
			defer manager.cleanupWg.Done()
			ticker := time.NewTicker(options.CleanupInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					manager.CleanExpiredConversations(options.ConversationTTL)
					manager.cleanExpiredTasks(options.TaskTTL)
				case <-manager.stopCleanup:
					return
				}
			}
		}()
	}

	return manager, nil
}

// =============================================================================
// TaskManager interface implementation
// =============================================================================

// OnSendMessage handles the message/send request. It runs the MessageProcessor and
// derives the result from the drained event stream: the task snapshot when a
// task exists, otherwise the last emitted Message (§3.1). With
// returnImmediately=true it returns on the immediate result while the
// execution continues in the background.
func (m *TaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	log.Debugf("TaskManager: OnSendMessage for message %s", request.Message.MessageID)

	eng, err := m.startExecution(ctx, &request, false)
	if err != nil {
		return nil, err
	}

	historyLength := historyLengthFromConfig(request.Configuration)
	if !request.Configuration.IsBlocking() {
		// v1.0 returnImmediately: answer with the immediate result — the
		// first persisted task snapshot or the first Message — and let the
		// engine keep running; later state is retrievable via GetTask/subscribe.
		select {
		case out := <-eng.immediateResult:
			return m.buildSendResponse(out.task, out.message, historyLength)
		case <-eng.done:
			// The stream closed before any immediate result: same derivation as blocking.
			return m.buildSendResponse(eng.finalTask, eng.lastMessage, historyLength)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case <-eng.done:
		return m.buildSendResponse(eng.finalTask, eng.lastMessage, historyLength)
	case <-ctx.Done():
		// The request died first. The execution is detached (§3.3): it keeps
		// running and its results stay retrievable via GetTask/resubscribe.
		return nil, ctx.Err()
	}
}

// OnSendMessageStream handles the message/stream request. The returned channel
// carries every MessageProcessor event in order, each persisted before delivery
// (§3.6), and is closed when the engine finishes — including after the
// synthetic terminal status from the close rules (§3.5).
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	log.Debugf("TaskManager: OnSendMessageStream for message %s", request.Message.MessageID)

	eng, err := m.startExecution(ctx, &request, true)
	if err != nil {
		return nil, err
	}
	// Tie the pipe to the request: the execution itself stays detached (§3.3),
	// but once the stream's consumer is gone the pipe has no reader — close it
	// so a blocking-send engine can never park on it and the server-side drain
	// ends instead of leaking.
	pipe := eng.pipe
	go func() {
		select {
		case <-ctx.Done():
			pipe.Close()
		case <-pipe.done:
		}
	}()
	return eng.pipe.Channel(), nil
}

// OnGetTask handles the tasks/get request
func (m *TaskManager) OnGetTask(ctx context.Context, params protocol.TaskQueryParams) (*protocol.Task, error) {
	m.taskMu.RLock()
	task, exists := m.tasks[params.ID]
	if !exists {
		m.taskMu.RUnlock()
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// return a copy of the task
	taskCopy := copyTask(task)
	m.taskMu.RUnlock()

	m.fillTaskHistory(taskCopy, params.HistoryLength)
	return taskCopy, nil
}

// OnCancelTask handles the tasks/cancel request. With a live execution it
// cancels the MessageProcessor's ctx and returns the current snapshot: the terminal
// CANCELED state is persisted later by the engine's close rule — unless the
// MessageProcessor still emits its own terminal status (completed/failed), which wins
// (§3.3: it may genuinely have finished first). Without a live execution the
// manager persists CANCELED directly, holding the execution slot with a
// sentinel so no continuation can start (and write) concurrently.
func (m *TaskManager) OnCancelTask(ctx context.Context, params protocol.TaskIDParams) (*protocol.Task, error) {
	for {
		live, sentinel := m.claimCancelSlot(params.ID)
		if live == nil {
			defer m.deregisterExecution(params.ID, sentinel)
			return m.cancelWithoutLiveRun(params)
		}

		// A stored terminal state is immutable even while the round is still
		// draining: canceling it must fail like the no-live path does.
		m.taskMu.RLock()
		task, exists := m.tasks[params.ID]
		var snapshot *protocol.Task
		var terminal protocol.TaskState
		if exists {
			if isFinalState(task.Status.State) {
				terminal = task.Status.State
			}
			snapshot = copyTask(task)
		}
		m.taskMu.RUnlock()
		if terminal != "" {
			return nil, taskmanager.ErrTaskNotCancelable(params.ID, terminal)
		}

		// Flag before canceling so the engine's close rule sees the request even
		// when the MessageProcessor reacts by closing the channel immediately.
		live.cancelRequested.Store(true)
		live.cancel()

		if m.liveExecution(params.ID) != live {
			// The run yielded (suspend) or finished while we were canceling, so
			// its close rule will not persist CANCELED on our behalf. Reassess:
			// the next pass either claims the free slot and persists CANCELED
			// itself, or cancels the continuation that took the slot.
			continue
		}

		if snapshot == nil {
			// Execution registered but no task materialized yet (§3.2 lazy creation).
			return nil, taskmanager.ErrTaskNotFound(params.ID)
		}
		return snapshot, nil
	}
}

// cancelWithoutLiveRun persists CANCELED for a task with no live execution:
// persist first, then broadcast (§3.6). The caller holds the task's execution
// slot (sentinel), making this the task's single writer.
func (m *TaskManager) cancelWithoutLiveRun(params protocol.TaskIDParams) (*protocol.Task, error) {
	m.taskMu.Lock()
	task, exists := m.tasks[params.ID]
	if !exists {
		m.taskMu.Unlock()
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}
	if isFinalState(task.Status.State) {
		state := task.Status.State
		m.taskMu.Unlock()
		return nil, taskmanager.ErrTaskNotCancelable(params.ID, state)
	}
	event := &protocol.TaskStatusUpdateEvent{
		TaskID:    params.ID,
		ContextID: task.ContextID,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCanceled,
			Timestamp: nowTimestamp(),
		},
		Final: true,
	}
	task.Status = event.Status
	result := copyTask(task)
	m.taskMu.Unlock()

	m.notifySubscribers(params.ID, protocol.NewStreamResponseStatusUpdate(event))
	m.cleanSubscribers(params.ID)
	return result, nil
}

// OnPushNotificationSet handles tasks/pushNotificationConfig/set requests
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	if m.pushSender == nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	// A Set without an explicit config ID replaces the task's default config
	// (keyed by the task ID) instead of appending a new one, matching the
	// pre-refactor overwrite semantics and avoiding duplicate deliveries.
	// Clients that want multiple configs for a task pass distinct IDs.
	if params.ID == "" {
		params.ID = params.TaskID
	}
	stored, err := m.pushStore.save(params)
	if err != nil {
		return nil, err
	}
	log.Debugf("TaskManager: Push notification config %s set for task %s", stored.ID, stored.TaskID)
	return &stored, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests
func (m *TaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	if m.pushSender == nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	// The TaskManager interface addresses Get by task ID only, so return the
	// task's first registered config. Per-config-ID Get needs the v1.0
	// GetTaskPushNotificationConfigParams and is a separate interface change.
	configs := m.pushStore.list(params.ID)
	if len(configs) == 0 {
		// Distinguish "task does not exist" from "task exists but has no config":
		// reporting -32001 for the latter would falsely tell the client the task
		// vanished.
		m.taskMu.RLock()
		_, taskExists := m.tasks[params.ID]
		m.taskMu.RUnlock()
		if !taskExists {
			return nil, taskmanager.ErrTaskNotFound(params.ID)
		}
		return nil, taskmanager.ErrPushConfigNotFound(params.ID)
	}
	config := configs[0]
	return &config, nil
}

// OnListTasks handles the v1.0 ListTasks request with filtering and offset-based pagination.
func (m *TaskManager) OnListTasks(
	ctx context.Context,
	params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	afterTime, err := taskmanager.ParseListTasksStatusTimestampAfter(params.StatusTimestampAfter)
	if err != nil {
		return nil, err
	}

	// Hold the read lock through pagination: taskmanager.PaginateTasks copies each returned
	// task, so the lock guards those copies against concurrent mutation.
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	var filtered []*protocol.Task
	for _, task := range m.tasks {
		if taskmanager.TaskMatchesListFilter(task, params, afterTime) {
			filtered = append(filtered, task)
		}
	}
	return taskmanager.PaginateTasks(filtered, params)
}

// OnPushNotificationList handles the v1.0 ListTaskPushNotificationConfigs request.
// It returns every push-notification config registered for the task.
func (m *TaskManager) OnPushNotificationList(
	ctx context.Context,
	params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	if m.pushSender == nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	configs := m.pushStore.list(params.TaskID)
	return &protocol.ListTaskPushNotificationConfigsResult{Configs: configs}, nil
}

// OnPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig request.
// Deleting a non-existent configuration is a no-op.
func (m *TaskManager) OnPushNotificationDelete(
	ctx context.Context,
	params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	if m.pushSender == nil {
		return taskmanager.ErrPushNotificationNotSupported()
	}
	// v1.0 addresses a specific config by ID; when omitted, remove all configs
	// for the task (legacy whole-task delete).
	if params.ID != "" {
		m.pushStore.remove(params.TaskID, params.ID)
		log.Debugf("TaskManager: Push notification config %s deleted for task %s", params.ID, params.TaskID)
		return nil
	}
	m.pushStore.removeAll(params.TaskID)
	log.Debugf("TaskManager: All push notification configs deleted for task %s", params.TaskID)
	return nil
}

// OnResubscribe handles tasks/resubscribe requests
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	// Check if task exists
	task, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// v1.0: a subscription is only valid for a non-terminal task; a task already
	// in a terminal state must be rejected with UnsupportedOperationError.
	if isFinalState(task.Status.State) {
		return nil, taskmanager.ErrUnsupportedOperation(
			fmt.Sprintf("subscribe to task %s in terminal state %s", params.ID, task.Status.State))
	}

	subscriber := newTaskSubscriber(
		params.ID,
		m.options.TaskSubscriberBufSize,
		m.options.TaskSubscriberBlockingSend,
	)

	// v1.0: the first stream event must be the current Task snapshot. Send it
	// before publishing the subscriber so it always precedes live updates (we
	// hold taskMu, so the engine cannot persist+broadcast in between).
	if err := subscriber.Send(protocol.NewStreamResponseTask(copyTask(task))); err != nil {
		subscriber.Close()
		return nil, err
	}

	// Add to subscribers list
	m.subscribers[params.ID] = append(m.subscribers[params.ID], subscriber)

	// Tie the subscription to the request: when the client goes away the
	// subscriber is removed and closed, so the server-side drain ends and the
	// slot is not leaked (a suspended task may never reach a terminal state
	// that would clean it).
	go func() {
		select {
		case <-ctx.Done():
			m.cleanupFailedSubscribers(params.ID, []*taskSubscriber{subscriber})
		case <-subscriber.done:
		}
	}()

	return subscriber.Channel(), nil
}

// =============================================================================
// Internal helper methods
// =============================================================================

// storeMessage stores messages
func (m *TaskManager) storeMessage(message protocol.Message) {
	m.conversationMu.Lock()
	defer m.conversationMu.Unlock()

	// Store the message
	m.messages[message.MessageID] = message

	// If the message has a contextID, add it to conversation history
	if message.ContextID != nil {
		contextID := *message.ContextID
		if _, exists := m.conversations[contextID]; !exists {
			m.conversations[contextID] = &ConversationHistory{
				MessageIDs:     make([]string, 0),
				LastAccessTime: time.Now(),
			}
		}

		// Add message ID to conversation history
		m.conversations[contextID].MessageIDs = append(m.conversations[contextID].MessageIDs, message.MessageID)
		// Update last access time
		m.conversations[contextID].LastAccessTime = time.Now()

		// Limit history length
		if len(m.conversations[contextID].MessageIDs) > m.options.MaxHistoryLength {
			// Remove the oldest message
			removedMsgID := m.conversations[contextID].MessageIDs[0]
			m.conversations[contextID].MessageIDs = m.conversations[contextID].MessageIDs[1:]
			// Delete old message from message storage
			delete(m.messages, removedMsgID)
		}
	}
}

// getConversationHistory gets conversation history of specified length
func (m *TaskManager) getConversationHistory(contextID string, length int) []protocol.Message {
	// LastAccessTime is mutated below: this must be the write lock (a read
	// lock here races concurrent history reads and tears the time value).
	m.conversationMu.Lock()
	defer m.conversationMu.Unlock()

	var history []protocol.Message

	if conversation, exists := m.conversations[contextID]; exists {
		// Update last access time
		conversation.LastAccessTime = time.Now()

		start := 0
		if len(conversation.MessageIDs) > length {
			start = len(conversation.MessageIDs) - length
		}

		for i := start; i < len(conversation.MessageIDs); i++ {
			if msg, exists := m.messages[conversation.MessageIDs[i]]; exists {
				history = append(history, msg)
			}
		}
	}

	return history
}

// fillTaskHistory fills the task copy's history per the v1.0 historyLength
// semantics:
//   - historyLength unset -> no limit (full history)
//   - historyLength == 0  -> no messages
//   - historyLength > 0   -> the most recent N messages
func (m *TaskManager) fillTaskHistory(task *protocol.Task, historyLength *int) {
	if task.ContextID == "" {
		return
	}
	switch {
	case historyLength == nil:
		task.History = m.getConversationHistory(task.ContextID, unlimitedHistoryLength)
	case *historyLength > 0:
		task.History = m.getConversationHistory(task.ContextID, *historyLength)
	default: // == 0 (or negative): no messages
		task.History = nil
	}
}

// processReplyMessage processes the reply message, add messageID and contextID if not set
func (m *TaskManager) processReplyMessage(ctxID *string, message *protocol.Message) {
	message.ContextID = ctxID
	message.Role = protocol.MessageRoleAgent

	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}

	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}

	m.storeMessage(*message)
}

// isFinalState checks if it's a final state
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed ||
		state == protocol.TaskStateCanceled ||
		state == protocol.TaskStateRejected
}

// isSuspendedState reports whether the state suspends the task awaiting a
// follow-up message (§3.4): the round has stopped working, but the task lives
// on for a continuation.
func isSuspendedState(state protocol.TaskState) bool {
	return state == protocol.TaskStateInputRequired ||
		state == protocol.TaskStateAuthRequired
}

// copyTask returns a copy of the task with its own Artifacts/History slices so
// callers can hand it out without racing later engine writes.
func copyTask(task *protocol.Task) *protocol.Task {
	taskCopy := *task
	if task.Artifacts != nil {
		taskCopy.Artifacts = append([]protocol.Artifact(nil), task.Artifacts...)
	}
	if task.History != nil {
		taskCopy.History = append([]protocol.Message(nil), task.History...)
	}
	return &taskCopy
}

// nowTimestamp returns the current UTC time in RFC3339, the format used for
// all task status timestamps.
func nowTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// PushSender returns the Sender configured via WithPushNotifications, or nil
// when push is not enabled. The A2AServer probes this accessor to advertise the
// pushNotifications capability on served agent cards when a Sender is present.
// (The JWKS signing identity is configured separately, via the server's
// WithPushNotificationAuthenticator.) A decorator TaskManager wrapping this one
// must forward this method, or the server cannot see the push configuration.
func (m *TaskManager) PushSender() push.Sender {
	return m.pushSender
}

// dispatchPush delivers event to every push webhook registered for taskID.
// It is a no-op unless a Sender is configured, and stays silent in manual
// delivery mode (the agent pushes on its own schedule). Delivery is
// asynchronous and best-effort: a slow or failing webhook must not block task
// event processing.
func (m *TaskManager) dispatchPush(taskID string, event protocol.StreamResponse) {
	if m.pushSender == nil || m.options.Push.ManualDelivery {
		return
	}
	if !pushWorthy(event) {
		return
	}
	configs := m.pushStore.list(taskID)
	if len(configs) == 0 {
		return
	}
	// Reserve the deliveries on pushWg under execMu together with the closed
	// check. Close sets m.closed under execMu before it waits on pushWg, so once
	// shutdown has begun no new Add can race that Wait. This matters because
	// dispatch is also reached from the OnCancelTask RPC path, which engineWg
	// does not track.
	m.execMu.Lock()
	if m.closed {
		m.execMu.Unlock()
		return
	}
	m.pushWg.Add(len(configs))
	m.execMu.Unlock()

	for i := range configs {
		cfg := configs[i]
		go func() {
			defer m.pushWg.Done()
			if err := m.pushSender.SendPush(m.pushCtx, cfg, event); err != nil {
				log.Warnf("push dispatch: send to %s for task %s: %v", cfg.URL, taskID, err)
			}
		}()
	}
}

// pushWorthy reports whether an event represents a task update worth pushing.
// A status update that carries a message payload is always delivered (a
// disconnected push client would otherwise miss agent output). Only content-less
// working/submitted heartbeats are skipped, to avoid webhook storms; terminal,
// input-required and auth-required transitions, along with task, message and
// artifact events, are delivered.
func pushWorthy(event protocol.StreamResponse) bool {
	if su := event.GetStatusUpdate(); su != nil {
		if su.Status.Message != nil {
			return true
		}
		switch su.Status.State {
		case protocol.TaskStateWorking, protocol.TaskStateSubmitted, protocol.TaskStateUnspecified:
			return false
		default:
			return true
		}
	}
	return true
}

// notifySubscribers notifies all subscribers of the task
func (m *TaskManager) notifySubscribers(taskID string, event protocol.StreamResponse) {
	// Deliver push notifications independently of live SSE subscribers: reaching
	// clients that are not currently streaming is the whole point of push.
	m.dispatchPush(taskID, event)

	m.taskMu.RLock()
	subs, exists := m.subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.taskMu.RUnlock()
		return
	}

	subsCopy := make([]*taskSubscriber, len(subs))
	copy(subsCopy, subs)
	m.taskMu.RUnlock()

	log.Debugf("Notifying %d subscribers for task %s", len(subsCopy), taskID)

	var failedSubscribers []*taskSubscriber

	for _, sub := range subsCopy {
		if sub.Closed() {
			log.Debugf("Subscriber for task %s is already closed, marking for removal", taskID)
			failedSubscribers = append(failedSubscribers, sub)
			continue
		}

		err := sub.Send(event)
		if err != nil {
			log.Warnf("Failed to send event to subscriber for task %s: %v", taskID, err)
			failedSubscribers = append(failedSubscribers, sub)
		}
	}

	// Clean up failed or closed subscribers
	if len(failedSubscribers) > 0 {
		m.cleanupFailedSubscribers(taskID, failedSubscribers)
	}
}

// cleanupFailedSubscribers cleans up failed or closed subscribers
func (m *TaskManager) cleanupFailedSubscribers(taskID string, failedSubscribers []*taskSubscriber) {
	m.taskMu.Lock()

	subs, exists := m.subscribers[taskID]
	if !exists {
		m.taskMu.Unlock()
		return
	}

	// Filter out failed subscribers
	filteredSubs := make([]*taskSubscriber, 0, len(subs))
	removedSubs := make([]*taskSubscriber, 0, len(failedSubscribers))

	for _, sub := range subs {
		shouldRemove := false
		for _, failedSub := range failedSubscribers {
			if sub == failedSub {
				shouldRemove = true
				removedSubs = append(removedSubs, sub)
				break
			}
		}
		if !shouldRemove {
			filteredSubs = append(filteredSubs, sub)
		}
	}

	if len(removedSubs) > 0 {
		m.subscribers[taskID] = filteredSubs
		log.Debugf("Removed %d failed subscribers for task %s", len(removedSubs), taskID)

		// If there are no subscribers left, delete the entire entry
		if len(filteredSubs) == 0 {
			delete(m.subscribers, taskID)
		}
	}
	m.taskMu.Unlock()

	for _, sub := range removedSubs {
		sub.Close()
	}
}

// cleanSubscribers closes and removes all subscribers for a task.
func (m *TaskManager) cleanSubscribers(taskID string) {
	m.taskMu.Lock()

	subs, exists := m.subscribers[taskID]
	if !exists {
		m.taskMu.Unlock()
		return
	}
	delete(m.subscribers, taskID)
	m.taskMu.Unlock()

	for _, sub := range subs {
		sub.Close()
	}
	log.Debugf("Cleaned subscribers for task %s", taskID)
}

// CleanExpiredConversations cleans up expired conversation history
// maxAge: the maximum lifetime of the conversation, conversations not accessed beyond this time will be cleaned up
func (m *TaskManager) CleanExpiredConversations(maxAge time.Duration) int {
	m.conversationMu.Lock()
	defer m.conversationMu.Unlock()

	now := time.Now()
	expiredContexts := make([]string, 0)
	expiredMessageIDs := make([]string, 0)

	// Find expired conversations
	for contextID, conversation := range m.conversations {
		if now.Sub(conversation.LastAccessTime) > maxAge {
			expiredContexts = append(expiredContexts, contextID)
			expiredMessageIDs = append(expiredMessageIDs, conversation.MessageIDs...)
		}
	}

	// Delete expired conversations
	for _, contextID := range expiredContexts {
		delete(m.conversations, contextID)
	}

	// Delete messages from expired conversations
	for _, messageID := range expiredMessageIDs {
		delete(m.messages, messageID)
	}

	if len(expiredContexts) > 0 {
		log.Debugf("Cleaned %d expired conversations, removed %d messages",
			len(expiredContexts), len(expiredMessageIDs))
	}

	return len(expiredContexts)
}

// cleanExpiredTasks cleans up tasks that have been in a terminal state longer than maxAge.
// It removes the task, its subscribers, and associated push notification configs.
// A maxAge of 0 disables cleanup and returns immediately.
func (m *TaskManager) cleanExpiredTasks(maxAge time.Duration) int {
	if maxAge <= 0 {
		return 0
	}
	m.taskMu.Lock()

	now := time.Now()
	expiredTaskIDs := make([]string, 0)
	subsToClose := make([]*taskSubscriber, 0)

	for taskID, task := range m.tasks {
		if !isFinalState(task.Status.State) {
			continue
		}
		ts, err := time.Parse(time.RFC3339, task.Status.Timestamp)
		if err != nil {
			// A terminal task with an unparsable/empty timestamp would never be
			// cleaned up. This is unreachable via the public API today (all writers
			// use RFC3339), but log it so a future bad-timestamp path is observable
			// instead of leaking silently.
			log.Debugf("Skipping task %s in cleanup: unparsable timestamp %q: %v",
				taskID, task.Status.Timestamp, err)
			continue
		}
		if now.Sub(ts) > maxAge {
			expiredTaskIDs = append(expiredTaskIDs, taskID)
		}
	}

	for _, taskID := range expiredTaskIDs {
		delete(m.tasks, taskID)
		if subs, exists := m.subscribers[taskID]; exists {
			subsToClose = append(subsToClose, subs...)
			delete(m.subscribers, taskID)
		}
	}

	m.taskMu.Unlock()

	for _, sub := range subsToClose {
		sub.Close()
	}

	if len(expiredTaskIDs) > 0 {
		log.Debugf("Cleaned %d expired tasks", len(expiredTaskIDs))

		// A terminal task normally has no live execution, but its engine may
		// still be draining; cancel the MessageProcessor ctx so a stuck run can exit.
		for _, taskID := range expiredTaskIDs {
			if exec := m.liveExecution(taskID); exec != nil {
				exec.cancel()
			}
		}

		for _, taskID := range expiredTaskIDs {
			m.pushStore.removeAll(taskID)
		}
	}

	return len(expiredTaskIDs)
}

// Close stops the cleanup goroutine and releases all resources.
// It refuses new runs, cancels every live MessageProcessor run, closes all streams,
// and waits for the detached engines to wind down (their close-rule persists
// land before teardown). A MessageProcessor is expected to close its channel once
// its ctx is canceled; Close blocks until every run has.
// It is safe to call Close multiple times; it always returns nil.
func (m *TaskManager) Close() error {
	m.closeOnce.Do(func() {
		// Signal the cleanup goroutine to stop and wait for it to exit so that no
		// tick runs concurrently with the teardown below.
		close(m.stopCleanup)
		m.cleanupWg.Wait()

		// Refuse new runs, request cancellation of every live one, and collect
		// their stream pipes: closing a pipe unblocks an engine parked on a
		// blocking pipe send.
		m.execMu.Lock()
		m.closed = true
		pipes := make([]*taskSubscriber, 0, len(m.executions))
		for _, exec := range m.executions {
			exec.cancelRequested.Store(true)
			exec.cancel()
			if exec.pipe != nil {
				pipes = append(pipes, exec.pipe)
			}
		}
		m.execMu.Unlock()
		for _, pipe := range pipes {
			pipe.Close()
		}

		// Close all fan-out subscribers (outside taskMu, so a stuck blocking
		// send can never wedge the manager-wide lock) — engine broadcasts must
		// not be able to block either.
		m.taskMu.Lock()
		subsToClose := make([]*taskSubscriber, 0)
		for _, subs := range m.subscribers {
			subsToClose = append(subsToClose, subs...)
		}
		m.subscribers = make(map[string][]*taskSubscriber)
		m.taskMu.Unlock()
		for _, sub := range subsToClose {
			sub.Close()
		}

		// Wait for the detached engines: their final persists (close-rule
		// CANCELED) land before teardown, and nothing re-populates the maps
		// afterwards.
		m.engineWg.Wait()

		// New push deliveries are fenced by m.closed (set above under execMu and
		// checked in dispatchPush before any pushWg.Add), so no Add can race this
		// Wait. Cancel the in-flight ones so a slow webhook cannot stall shutdown,
		// then drain them.
		m.pushCancel()
		m.pushWg.Wait()

		m.taskMu.Lock()
		m.tasks = make(map[string]*protocol.Task)
		m.taskMu.Unlock()
	})
	return nil
}

// GetConversationStats gets conversation statistics
func (m *TaskManager) GetConversationStats() map[string]interface{} {
	m.conversationMu.RLock()
	defer m.conversationMu.RUnlock()

	totalConversations := len(m.conversations)
	totalMessages := len(m.messages)

	oldestAccess := time.Now()
	newestAccess := time.Time{}

	for _, conversation := range m.conversations {
		if conversation.LastAccessTime.Before(oldestAccess) {
			oldestAccess = conversation.LastAccessTime
		}
		if conversation.LastAccessTime.After(newestAccess) {
			newestAccess = conversation.LastAccessTime
		}
	}

	stats := map[string]interface{}{
		"total_conversations": totalConversations,
		"total_messages":      totalMessages,
	}

	if totalConversations > 0 {
		stats["oldest_access"] = oldestAccess
		stats["newest_access"] = newestAccess
	}

	return stats
}
