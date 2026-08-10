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
	"errors"
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

// scopedID is the internal identity of tenant-and-owner-scoped data. Neither
// scope component is written into protocol.Task.
type scopedID struct {
	tenant string
	owner  string
	id     string
}

func newScopedID(tenant, owner, id string) scopedID {
	return scopedID{tenant: tenant, owner: owner, id: id}
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

	// messages stores all Messages, indexed by tenant, owner, and message ID.
	messages map[scopedID]protocol.Message

	// conversations stores the message history of each conversation, indexed by contextID
	// key: contextID, value: ConversationHistory
	conversations map[scopedID]*ConversationHistory

	// conversationMu protects the messages and conversations fields
	conversationMu sync.RWMutex

	// tasks stores the task snapshots, indexed by taskID. The drain engine is
	// the writer while an execution is live; OnCancelTask writes only when no
	// execution is live (see OnCancelTask).
	tasks map[scopedID]*protocol.Task

	// taskMu protects the tasks and subscribers fields
	taskMu sync.RWMutex

	// subscribers stores the fan-out endpoints of each task's event stream
	// key: taskID, value: subscriber list
	subscribers map[scopedID][]*taskSubscriber

	// pushStore persists push-notification configs (multiple per task, addressed
	// by config ID). It is an internal detail; pluggable storage backends are
	// not part of the public API yet.
	pushStore *pushConfigStore

	// pushEnabled controls config registration and capability advertisement.
	// Automatic delivery is a separate concern owned by pushDispatcher.
	pushEnabled bool

	// pushDispatcher owns the bounded, ordered automatic-delivery workers. It is
	// nil when push is disabled or the agent selected manual delivery.
	pushDispatcher *push.Dispatcher

	// runs is the sole owner of live MessageProcessor slots (admission,
	// suspend handoff, cancellation, Close). See executionRegistry.
	runs *executionRegistry

	// options
	options *TaskManagerOptions

	// stopCleanup signals the cleanup goroutine to stop
	stopCleanup chan struct{}
	// cleanupWg tracks the cleanup goroutine so Close can wait for it to exit
	cleanupWg sync.WaitGroup
	closeOnce sync.Once
}

var _ taskmanager.TaskManager = (*TaskManager)(nil)

func (m *TaskManager) resolveOwner(ctx context.Context) (string, error) {
	if m.options.OwnerResolver == nil {
		return "", nil
	}
	owner, err := m.options.OwnerResolver(ctx)
	if err != nil {
		return "", taskmanager.ErrInternalError(fmt.Sprintf("resolve task owner: %v", err))
	}
	if owner == "" {
		return "", taskmanager.ErrInternalError("task owner resolver returned an empty owner")
	}
	return owner, nil
}

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
	if options.Push.MaxConcurrentDeliveries < 0 || options.Push.DeliveryQueueSize < 0 {
		return nil, fmt.Errorf("push delivery concurrency and queue size cannot be negative")
	}

	manager := &TaskManager{
		processor:     processor,
		messages:      make(map[scopedID]protocol.Message),
		conversations: make(map[scopedID]*ConversationHistory),
		tasks:         make(map[scopedID]*protocol.Task),
		subscribers:   make(map[scopedID][]*taskSubscriber),
		pushStore:     newPushConfigStore(),
		pushEnabled:   options.Push.Sender != nil || options.Push.ManualDelivery,
		runs:          newExecutionRegistry(),
		options:       options,
		stopCleanup:   make(chan struct{}),
	}
	if options.Push.Sender != nil && !options.Push.ManualDelivery {
		manager.pushDispatcher = push.NewDispatcher(
			context.Background(), options.Push.Sender,
			options.Push.MaxConcurrentDeliveries, options.Push.DeliveryQueueSize,
			manager.pushStore.isCurrent,
		)
	}

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
			return m.buildSendResponse(request.Tenant, eng.owner, out.task, out.message, historyLength)
		case <-eng.done:
			// The stream closed before any immediate result: same derivation as blocking.
			return m.buildSendResponse(request.Tenant, eng.owner, eng.finalOutcome.task, eng.finalOutcome.message, historyLength)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case <-eng.done:
		return m.buildSendResponse(request.Tenant, eng.owner, eng.finalOutcome.task, eng.finalOutcome.message, historyLength)
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	key := newScopedID(params.Tenant, owner, params.ID)
	m.taskMu.RLock()
	task, exists := m.tasks[key]
	if !exists {
		m.taskMu.RUnlock()
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// return a copy of the task
	taskCopy := copyTask(task)
	m.taskMu.RUnlock()

	m.fillTaskHistory(params.Tenant, owner, taskCopy, params.HistoryLength)
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	for {
		live, sentinel, yieldDone := m.runs.claimCancelSlot(params.Tenant, owner, params.ID)
		if yieldDone != nil {
			select {
			case <-yieldDone:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if live == nil {
			defer m.runs.deregister(params.Tenant, owner, params.ID, sentinel)
			return m.cancelWithoutLiveRun(owner, params)
		}

		// A stored terminal state is immutable even while the round is still
		// draining: canceling it must fail like the no-live path does.
		m.taskMu.RLock()
		task, exists := m.tasks[newScopedID(params.Tenant, owner, params.ID)]
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

		// Linearize cancellation against a concurrent suspend handoff. If the
		// handoff won, wait for it and retry as a no-live cancel; otherwise the
		// close rule is guaranteed to observe cancelRequested.
		yieldDone, accepted := m.runs.requestCancel(params.Tenant, owner, params.ID, live)
		if yieldDone != nil {
			select {
			case <-yieldDone:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if !accepted {
			continue
		}

		if m.runs.live(params.Tenant, owner, params.ID) != live {
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
func (m *TaskManager) cancelWithoutLiveRun(owner string, params protocol.TaskIDParams) (*protocol.Task, error) {
	key := newScopedID(params.Tenant, owner, params.ID)
	m.taskMu.Lock()
	task, exists := m.tasks[key]
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

	m.notifySubscribers(params.Tenant, owner, params.ID, protocol.NewStreamResponseStatusUpdate(event))
	m.cleanSubscribers(params.Tenant, owner, params.ID)
	return result, nil
}

// OnPushNotificationSet handles tasks/pushNotificationConfig/set requests
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if err := push.ValidateConfig(params); err != nil {
		return nil, taskmanager.ErrInvalidParams(err.Error())
	}
	if err := m.ensurePushTaskExists(params.Tenant, owner, params.TaskID); err != nil {
		return nil, err
	}
	stored, err := m.pushStore.save(owner, params)
	if err != nil {
		return nil, err
	}
	log.Debugf("TaskManager: Push notification config %s set for task %s", stored.ID, stored.TaskID)
	return &stored, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests
func (m *TaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.GetTaskPushNotificationConfigParams,
) (*protocol.TaskPushNotificationConfig, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		return nil, taskmanager.ErrInvalidParams("push notification config ID is required")
	}
	if err := m.ensurePushTaskExists(params.Tenant, owner, params.TaskID); err != nil {
		return nil, err
	}
	config, found := m.pushStore.get(params.Tenant, owner, params.TaskID, params.ID)
	if !found {
		return nil, taskmanager.ErrPushConfigNotFound(params.TaskID)
	}
	return &config, nil
}

// OnListTasks handles the v1.0 ListTasks request with filtering and keyset pagination.
func (m *TaskManager) OnListTasks(
	ctx context.Context,
	params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	afterTime, err := taskmanager.ParseListTasksStatusTimestampAfter(params.StatusTimestampAfter)
	if err != nil {
		return nil, err
	}

	// Hold the read lock through pagination: taskmanager.PaginateTasks copies each returned
	// task, so the lock guards those copies against concurrent mutation.
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	var filtered []*protocol.Task
	for key, task := range m.tasks {
		if key.tenant != params.Tenant || key.owner != owner {
			continue
		}
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if err := m.ensurePushTaskExists(params.Tenant, owner, params.TaskID); err != nil {
		return nil, err
	}
	configs := m.pushStore.list(params.Tenant, owner, params.TaskID)
	return &protocol.ListTaskPushNotificationConfigsResult{Configs: configs}, nil
}

// OnPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig request.
// Deleting a non-existent configuration is a no-op.
func (m *TaskManager) OnPushNotificationDelete(
	ctx context.Context,
	params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return err
	}
	if !m.pushEnabled {
		return taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		return taskmanager.ErrInvalidParams("push notification config ID is required")
	}
	if err := m.ensurePushTaskExists(params.Tenant, owner, params.TaskID); err != nil {
		return err
	}
	m.pushStore.remove(params.Tenant, owner, params.TaskID, params.ID)
	log.Debugf("TaskManager: Push notification config %s deleted for task %s", params.ID, params.TaskID)
	return nil
}

func (m *TaskManager) ensurePushTaskExists(tenant, owner, taskID string) error {
	m.taskMu.RLock()
	_, exists := m.tasks[newScopedID(tenant, owner, taskID)]
	m.taskMu.RUnlock()
	if !exists {
		return taskmanager.ErrTaskNotFound(taskID)
	}
	return nil
}

// OnResubscribe handles tasks/resubscribe requests
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	// Close marks the execution registry closed before sweeping subscribers.
	// Check that state while holding taskMu so a subscriber admitted just before
	// shutdown is necessarily included in the sweep, while one arriving after
	// shutdown is rejected instead of leaking past Close.
	if m.runs.isClosed() {
		return nil, taskmanager.ErrInternalError("task manager is closed")
	}
	key := newScopedID(params.Tenant, owner, params.ID)

	// Check if task exists
	task, exists := m.tasks[key]
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
	m.subscribers[key] = append(m.subscribers[key], subscriber)

	// Tie the subscription to the request: when the client goes away the
	// subscriber is removed and closed, so the server-side drain ends and the
	// slot is not leaked (a suspended task may never reach a terminal state
	// that would clean it).
	go func() {
		select {
		case <-ctx.Done():
			m.cleanupFailedSubscribers(params.Tenant, owner, params.ID, []*taskSubscriber{subscriber})
		case <-subscriber.done:
		}
	}()

	return subscriber.Channel(), nil
}

// =============================================================================
// Internal helper methods
// =============================================================================

// storeMessage stores messages
func (m *TaskManager) storeMessage(tenant, owner string, message protocol.Message) {
	m.conversationMu.Lock()
	defer m.conversationMu.Unlock()

	// Store the message
	m.messages[newScopedID(tenant, owner, message.MessageID)] = message

	// If the message has a contextID, add it to conversation history
	if message.ContextID != nil {
		contextID := *message.ContextID
		conversationKey := newScopedID(tenant, owner, contextID)
		conv, exists := m.conversations[conversationKey]
		if !exists {
			conv = &ConversationHistory{
				MessageIDs:     make([]string, 0),
				LastAccessTime: time.Now(),
			}
			m.conversations[conversationKey] = conv
		}
		conv.LastAccessTime = time.Now()

		// Idempotent by MessageID: the same message may reach storeMessage from
		// both the reply path and a status roll (or be re-emitted). Indexing it
		// twice would show it twice in history AND, worse, let a later window
		// trim delete the shared m.messages entry that the duplicate index still
		// references — dropping the message entirely.
		for _, id := range conv.MessageIDs {
			if id == message.MessageID {
				return
			}
		}

		// Add message ID to conversation history
		conv.MessageIDs = append(conv.MessageIDs, message.MessageID)

		// Limit history length
		if len(conv.MessageIDs) > m.options.MaxHistoryLength {
			// Remove the oldest message
			removedMsgID := conv.MessageIDs[0]
			conv.MessageIDs = conv.MessageIDs[1:]
			// Delete old message from message storage
			delete(m.messages, newScopedID(tenant, owner, removedMsgID))
		}
	}
}

// getConversationHistory gets conversation history of specified length
func (m *TaskManager) getConversationHistory(tenant, owner, contextID string, length int) []protocol.Message {
	// LastAccessTime is mutated below: this must be the write lock (a read
	// lock here races concurrent history reads and tears the time value).
	m.conversationMu.Lock()
	defer m.conversationMu.Unlock()

	var history []protocol.Message

	if conversation, exists := m.conversations[newScopedID(tenant, owner, contextID)]; exists {
		// Update last access time
		conversation.LastAccessTime = time.Now()

		start := 0
		if len(conversation.MessageIDs) > length {
			start = len(conversation.MessageIDs) - length
		}

		for i := start; i < len(conversation.MessageIDs); i++ {
			if msg, exists := m.messages[newScopedID(tenant, owner, conversation.MessageIDs[i])]; exists {
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
func (m *TaskManager) fillTaskHistory(tenant, owner string, task *protocol.Task, historyLength *int) {
	if task.ContextID == "" {
		return
	}
	switch {
	case historyLength == nil:
		task.History = m.getConversationHistory(tenant, owner, task.ContextID, unlimitedHistoryLength)
	case *historyLength > 0:
		task.History = m.getConversationHistory(tenant, owner, task.ContextID, *historyLength)
	default: // == 0 (or negative): no messages
		task.History = nil
	}
}

// processReplyMessage processes the reply message, add messageID and contextID if not set
func (m *TaskManager) processReplyMessage(tenant, owner string, ctxID *string, message *protocol.Message) {
	message.ContextID = ctxID
	message.Role = protocol.MessageRoleAgent

	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}

	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}

	m.storeMessage(tenant, owner, *message)
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

// SupportsPushNotifications reports whether push registration and delivery are enabled.
func (m *TaskManager) SupportsPushNotifications() bool {
	return m.pushEnabled
}

// dispatchPush delivers event to every push webhook registered for taskID.
// It is a no-op unless automatic delivery is configured. The bounded dispatcher
// preserves order per config; when its queue is full this call applies
// backpressure rather than dropping an event.
func (m *TaskManager) dispatchPush(tenant, owner, taskID string, event protocol.StreamResponse) {
	if m.pushDispatcher == nil {
		return
	}
	registrations := m.pushStore.registrations(tenant, owner, taskID)
	if len(registrations) == 0 {
		return
	}
	if m.runs.isClosed() {
		return
	}
	if err := m.pushDispatcher.Enqueue(registrations, event); err != nil && !errors.Is(err, push.ErrDispatcherClosed) {
		log.Warnf("push dispatch: enqueue for task %s: %v", taskID, err)
	}
}

// notifySubscribers notifies all subscribers of the task
func (m *TaskManager) notifySubscribers(tenant, owner, taskID string, event protocol.StreamResponse) {
	// Deliver push notifications independently of live SSE subscribers: reaching
	// clients that are not currently streaming is the whole point of push.
	m.dispatchPush(tenant, owner, taskID, event)
	m.notifyLiveSubscribers(tenant, owner, taskID, event)
}

// notifyLiveSubscribers fans out to process-local task subscribers without
// dispatching push a second time.
func (m *TaskManager) notifyLiveSubscribers(tenant, owner, taskID string, event protocol.StreamResponse) {
	key := newScopedID(tenant, owner, taskID)
	m.taskMu.RLock()
	subs, exists := m.subscribers[key]
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
		m.cleanupFailedSubscribers(tenant, owner, taskID, failedSubscribers)
	}
}

// cleanupFailedSubscribers cleans up failed or closed subscribers
func (m *TaskManager) cleanupFailedSubscribers(
	tenant, owner, taskID string,
	failedSubscribers []*taskSubscriber,
) {
	key := newScopedID(tenant, owner, taskID)
	m.taskMu.Lock()

	subs, exists := m.subscribers[key]
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
		m.subscribers[key] = filteredSubs
		log.Debugf("Removed %d failed subscribers for task %s", len(removedSubs), taskID)

		// If there are no subscribers left, delete the entire entry
		if len(filteredSubs) == 0 {
			delete(m.subscribers, key)
		}
	}
	m.taskMu.Unlock()

	for _, sub := range removedSubs {
		sub.Close()
	}
}

// cleanSubscribers closes and removes all subscribers for a task.
func (m *TaskManager) cleanSubscribers(tenant, owner, taskID string) {
	key := newScopedID(tenant, owner, taskID)
	m.taskMu.Lock()

	subs, exists := m.subscribers[key]
	if !exists {
		m.taskMu.Unlock()
		return
	}
	delete(m.subscribers, key)
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
	expiredContexts := make([]scopedID, 0)
	expiredMessages := make([]scopedID, 0)

	// Find expired conversations
	for contextKey, conversation := range m.conversations {
		if now.Sub(conversation.LastAccessTime) > maxAge {
			expiredContexts = append(expiredContexts, contextKey)
			for _, messageID := range conversation.MessageIDs {
				expiredMessages = append(expiredMessages, newScopedID(contextKey.tenant, contextKey.owner, messageID))
			}
		}
	}

	// Delete expired conversations
	for _, contextID := range expiredContexts {
		delete(m.conversations, contextID)
	}

	// Delete messages from expired conversations
	for _, messageKey := range expiredMessages {
		delete(m.messages, messageKey)
	}

	if len(expiredContexts) > 0 {
		log.Debugf("Cleaned %d expired conversations, removed %d messages",
			len(expiredContexts), len(expiredMessages))
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
	expiredTaskIDs := make([]scopedID, 0)
	subsToClose := make([]*taskSubscriber, 0)

	for taskKey, task := range m.tasks {
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
				taskKey.id, task.Status.Timestamp, err)
			continue
		}
		if now.Sub(ts) > maxAge {
			expiredTaskIDs = append(expiredTaskIDs, taskKey)
		}
	}

	for _, taskKey := range expiredTaskIDs {
		delete(m.tasks, taskKey)
		if subs, exists := m.subscribers[taskKey]; exists {
			subsToClose = append(subsToClose, subs...)
			delete(m.subscribers, taskKey)
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
		for _, taskKey := range expiredTaskIDs {
			if exec := m.runs.live(taskKey.tenant, taskKey.owner, taskKey.id); exec != nil {
				exec.cancel()
			}
		}

		for _, taskKey := range expiredTaskIDs {
			m.pushStore.removeAll(taskKey.tenant, taskKey.owner, taskKey.id)
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
		pipes := m.runs.shutdown()
		// Unblock an enqueue waiting on a full queue and cancel slow webhook
		// calls before waiting for engines that may be inside dispatch.
		m.pushDispatcher.Close()
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
		m.subscribers = make(map[scopedID][]*taskSubscriber)
		m.taskMu.Unlock()
		for _, sub := range subsToClose {
			sub.Close()
		}

		// Wait for the detached engines: their final persists (close-rule
		// CANCELED) land before teardown, and nothing re-populates the maps
		// afterwards.
		m.runs.wait()

		m.taskMu.Lock()
		m.tasks = make(map[scopedID]*protocol.Task)
		m.taskMu.Unlock()
		m.pushStore.close()
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
