// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package messageprocessor provides implementations for processing A2A messages.

package memory

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const defaultMaxHistoryLength = 100
const defaultCleanupInterval = 30 * time.Second
const defaultConversationTTL = 1 * time.Hour
const defaultTaskTTL = 0
const defaultSubscriberBufferSize = 1024

// ConversationHistory stores conversation history information
type ConversationHistory struct {
	// MessageIDs is the list of message IDs, ordered by time
	MessageIDs []string
	// LastAccessTime is the last access time
	LastAccessTime time.Time
}

// CancellableTask is a task that can be cancelled
type CancellableTask struct {
	task       protocol.Task
	cancelFunc context.CancelFunc
	ctx        context.Context
}

// NewCancellableTask creates a new cancellable task
func NewCancellableTask(task protocol.Task) *CancellableTask {
	cancelCtx, cancel := context.WithCancel(context.Background())
	return &CancellableTask{
		task:       task,
		cancelFunc: cancel,
		ctx:        cancelCtx,
	}
}

// Cancel cancels the task
func (t *CancellableTask) Cancel() {
	t.cancelFunc()
}

// Task returns the task
func (t *CancellableTask) Task() *protocol.Task {
	return &t.task
}

// TaskSubscriberOpts is the options for the TaskSubscriber
type TaskSubscriberOpts struct {
	sendHook     func(event protocol.StreamResponse) error
	blockingSend bool
}

// TaskSubscriberOption is the option for the TaskSubscriber
type TaskSubscriberOption func(s *TaskSubscriberOpts)

// WithSubscriberSendHook sets the send hook for the task subscriber
func WithSubscriberSendHook(hook func(event protocol.StreamResponse) error) TaskSubscriberOption {
	return func(s *TaskSubscriberOpts) {
		s.sendHook = hook
	}
}

// WithSubscriberBlockingSend sets the blocking send flag for the task subscriber
func WithSubscriberBlockingSend(blockingSend bool) TaskSubscriberOption {
	return func(s *TaskSubscriberOpts) {
		s.blockingSend = blockingSend
	}
}

// TaskSubscriber is a subscriber for a task
type TaskSubscriber struct {
	taskID         string
	eventQueue     chan protocol.StreamResponse
	done           chan struct{}
	lastAccessTime time.Time
	closed         atomic.Bool
	mu             sync.RWMutex
	opts           TaskSubscriberOpts
}

// NewTaskSubscriber creates a new task subscriber with specified buffer length
func NewTaskSubscriber(
	taskID string,
	bufSize int,
	opts ...TaskSubscriberOption,
) *TaskSubscriber {
	subscriberOpts := TaskSubscriberOpts{
		sendHook: nil,
	}
	for _, opt := range opts {
		opt(&subscriberOpts)
	}

	if bufSize <= 0 {
		bufSize = defaultSubscriberBufferSize // default buffer size
	}
	eventQueue := make(chan protocol.StreamResponse, bufSize)
	return &TaskSubscriber{
		taskID:         taskID,
		eventQueue:     eventQueue,
		done:           make(chan struct{}),
		lastAccessTime: time.Now(),
		closed:         atomic.Bool{},
		opts:           subscriberOpts,
	}
}

// Close closes the task subscriber
func (s *TaskSubscriber) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	close(s.done)

	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.eventQueue)
}

// Channel returns the channel of the task subscriber
func (s *TaskSubscriber) Channel() <-chan protocol.StreamResponse {
	return s.eventQueue
}

// Closed returns true if the task subscriber is closed
func (s *TaskSubscriber) Closed() bool {
	return s.closed.Load()
}

// Send sends an event to the task subscriber
func (s *TaskSubscriber) Send(event protocol.StreamResponse) error {
	if s.Closed() {
		return fmt.Errorf("task subscriber is closed")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Closed() {
		return fmt.Errorf("task subscriber is closed")
	}

	s.lastAccessTime = time.Now()
	if s.opts.sendHook != nil {
		err := s.opts.sendHook(event)
		if err != nil {
			return err
		}
	}

	if s.opts.blockingSend {
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

// GetLastAccessTime returns the last access time
func (s *TaskSubscriber) GetLastAccessTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccessTime
}

// TaskManager is the implementation of the TaskManager interface
type TaskManager struct {
	// mu protects the following fields
	mu sync.RWMutex

	// Processor is the user-provided message Processor
	Processor taskmanager.MessageProcessor

	// Messages stores all Messages, indexed by messageID
	// key: messageID, value: Message
	Messages map[string]protocol.Message

	// Conversations stores the message history of each conversation, indexed by contextID
	// key: contextID, value: ConversationHistory
	Conversations map[string]*ConversationHistory

	// conversationMu protects the Conversations field
	conversationMu sync.RWMutex

	// Tasks stores the task information, indexed by taskID
	// key: taskID, value: Task
	Tasks map[string]*CancellableTask

	// taskMu protects the Tasks field
	taskMu sync.RWMutex

	// Subscribers stores the task subscribers
	// key: taskID, value: TaskSubscriber list
	// supports all event types: Message, Task, TaskStatusUpdateEvent, TaskArtifactUpdateEvent
	Subscribers map[string][]*TaskSubscriber

	// PushNotifications stores the push notification configurations
	// key: taskID, value: push notification configuration
	PushNotifications map[string]protocol.TaskPushNotificationConfig

	// options
	options *TaskManagerOptions

	// stopCleanup signals the cleanup goroutine to stop
	stopCleanup chan struct{}
	// cleanupWg tracks the cleanup goroutine so Close can wait for it to exit
	cleanupWg sync.WaitGroup
	closeOnce sync.Once
}

// NewTaskManager creates a new TaskManager instance
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

	manager := &TaskManager{
		Processor:         processor,
		Messages:          make(map[string]protocol.Message),
		Conversations:     make(map[string]*ConversationHistory),
		Tasks:             make(map[string]*CancellableTask),
		Subscribers:       make(map[string][]*TaskSubscriber),
		PushNotifications: make(map[string]protocol.TaskPushNotificationConfig),
		options:           options,
		stopCleanup:       make(chan struct{}),
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

// OnSendMessage handles the message/tasks request
func (m *TaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	log.Debugf("TaskManager: OnSendMessage for message %s", request.Message.MessageID)

	// process the request message
	m.processRequestMessage(&request.Message)

	// process Configuration
	options := m.processConfiguration(request.Configuration)
	options.Streaming = false // non-streaming processing
	options.Tenant = request.Tenant

	// create MessageHandle
	handle := &taskHandler{
		manager:                m,
		messageID:              request.Message.MessageID,
		ctx:                    ctx,
		subscriberBufSize:      m.options.TaskSubscriberBufSize,
		subscriberBlockingSend: m.options.TaskSubscriberBlockingSend,
	}

	// call the user's message processor
	result, err := m.Processor.ProcessMessage(ctx, request.Message, options, handle)
	if err != nil {
		return nil, fmt.Errorf("message processing failed: %w", err)
	}

	if result == nil {
		return nil, fmt.Errorf("processor returned nil result")
	}

	// check if the user returned StreamingEvents for non-streaming request
	if result.StreamingEvents != nil {
		log.Infof("User returned StreamingEvents for non-streaming request, ignoring")
	}

	if result.Result == nil {
		return nil, fmt.Errorf("processor returned nil result for non-streaming request")
	}

	if result.Result.GetMessage() != nil {
		m.processReplyMessage(request.Message.ContextID, result.Result.GetMessage())
	}

	return result.Result, nil
}

// OnSendMessageStream handles message/stream requests
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	log.Debugf("TaskManager: OnSendMessageStream for message %s", request.Message.MessageID)

	m.processRequestMessage(&request.Message)

	// Process Configuration
	options := m.processConfiguration(request.Configuration)
	options.Streaming = true // streaming mode
	options.Tenant = request.Tenant

	// Create streaming MessageHandle
	handle := &taskHandler{
		manager:                m,
		messageID:              request.Message.MessageID,
		ctx:                    ctx,
		subscriberBufSize:      m.options.TaskSubscriberBufSize,
		subscriberBlockingSend: m.options.TaskSubscriberBlockingSend,
	}

	// Call user's message processor
	result, err := m.Processor.ProcessMessage(ctx, request.Message, options, handle)
	if err != nil {
		return nil, fmt.Errorf("message processing failed: %w", err)
	}

	if result == nil || result.StreamingEvents == nil {
		return nil, fmt.Errorf("processor returned nil result")
	}

	return result.StreamingEvents.Channel(), nil
}

// OnGetTask handles the tasks/get request
func (m *TaskManager) OnGetTask(ctx context.Context, params protocol.TaskQueryParams) (*protocol.Task, error) {
	m.taskMu.RLock()
	task, exists := m.Tasks[params.ID]
	if !exists {
		m.taskMu.RUnlock()
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// return a copy of the task
	taskCopy := *task.Task()
	m.taskMu.RUnlock()

	// Fill message history per the v1.0 GetTaskRequest semantics:
	//   - historyLength unset -> no limit (full history)
	//   - historyLength == 0  -> no messages
	//   - historyLength > 0   -> the most recent N messages
	if taskCopy.ContextID != "" {
		switch {
		case params.HistoryLength == nil:
			taskCopy.History = m.getConversationHistory(taskCopy.ContextID, unlimitedHistoryLength)
		case *params.HistoryLength > 0:
			taskCopy.History = m.getConversationHistory(taskCopy.ContextID, *params.HistoryLength)
		default: // == 0 (or negative): no messages
			taskCopy.History = nil
		}
	}

	return &taskCopy, nil
}

// OnCancelTask handles the tasks/cancel request
func (m *TaskManager) OnCancelTask(ctx context.Context, params protocol.TaskIDParams) (*protocol.Task, error) {
	m.taskMu.Lock()
	task, exists := m.Tasks[params.ID]
	if !exists {
		m.taskMu.Unlock()
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	taskCopy := *task.Task()
	m.taskMu.Unlock()

	handle := &taskHandler{
		manager:                m,
		ctx:                    ctx,
		subscriberBufSize:      m.options.TaskSubscriberBufSize,
		subscriberBlockingSend: m.options.TaskSubscriberBlockingSend,
	}
	handle.CleanTask(&params.ID)
	taskCopy.Status.State = protocol.TaskStateCanceled
	taskCopy.Status.Timestamp = time.Now().UTC().Format(time.RFC3339)

	return &taskCopy, nil
}

// OnPushNotificationSet handles tasks/pushNotificationConfig/set requests
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store push notification configuration
	m.PushNotifications[params.TaskID] = params
	log.Debugf("TaskManager: Push notification config set for task %s", params.TaskID)
	return &params, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests
func (m *TaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	config, exists := m.PushNotifications[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	return &config, nil
}

// unlimitedHistoryLength is used to request the full conversation history when
// a v1.0 request leaves historyLength unset (spec: unset means no limit).
const unlimitedHistoryLength = 1 << 30

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
	for _, ct := range m.Tasks {
		if task := ct.Task(); taskmanager.TaskMatchesListFilter(task, params, afterTime) {
			filtered = append(filtered, task)
		}
	}
	return taskmanager.PaginateTasks(filtered, params)
}

// OnPushNotificationList handles the v1.0 ListTaskPushNotificationConfigs request.
// The in-memory manager stores at most one configuration per task, so the result
// contains zero or one entries.
func (m *TaskManager) OnPushNotificationList(
	ctx context.Context,
	params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := &protocol.ListTaskPushNotificationConfigsResult{
		Configs: []protocol.TaskPushNotificationConfig{},
	}
	if config, exists := m.PushNotifications[params.TaskID]; exists {
		result.Configs = append(result.Configs, config)
	}
	return result, nil
}

// OnPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig request.
// Deleting a non-existent configuration is a no-op.
func (m *TaskManager) OnPushNotificationDelete(
	ctx context.Context,
	params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.PushNotifications, params.TaskID)
	log.Debugf("TaskManager: Push notification config deleted for task %s", params.TaskID)
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
	task, exists := m.Tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// v1.0: a subscription is only valid for a non-terminal task; a task already
	// in a terminal state must be rejected with UnsupportedOperationError.
	if isFinalState(task.Task().Status.State) {
		return nil, taskmanager.ErrUnsupportedOperation(
			fmt.Sprintf("subscribe to task %s in terminal state %s", params.ID, task.Task().Status.State))
	}

	bufSize := m.options.TaskSubscriberBufSize
	if bufSize <= 0 {
		bufSize = defaultSubscriberBufferSize
	}

	subscriber := NewTaskSubscriber(
		params.ID,
		bufSize,
		WithSubscriberBlockingSend(m.options.TaskSubscriberBlockingSend),
		WithSubscriberSendHook(m.sendStreamingEventHook(params.ID)),
	)

	// v1.0: the first stream event must be the current Task snapshot. Send it
	// before publishing the subscriber so it always precedes live updates (we
	// hold taskMu, so notifySubscribers cannot interleave).
	snapshot := *task.Task()
	if err := subscriber.Send(protocol.StreamResponse{Result: &snapshot}); err != nil {
		subscriber.Close()
		return nil, err
	}

	// Add to subscribers list
	if _, exists := m.Subscribers[params.ID]; !exists {
		m.Subscribers[params.ID] = make([]*TaskSubscriber, 0)
	}
	m.Subscribers[params.ID] = append(m.Subscribers[params.ID], subscriber)

	return subscriber.eventQueue, nil
}

// =============================================================================
// Internal helper methods
// =============================================================================

// storeMessage stores messages
func (m *TaskManager) storeMessage(message protocol.Message) {
	m.conversationMu.Lock()
	defer m.conversationMu.Unlock()

	// Store the message
	m.Messages[message.MessageID] = message

	// If the message has a contextID, add it to conversation history
	if message.ContextID != nil {
		contextID := *message.ContextID
		if _, exists := m.Conversations[contextID]; !exists {
			m.Conversations[contextID] = &ConversationHistory{
				MessageIDs:     make([]string, 0),
				LastAccessTime: time.Now(),
			}
		}

		// Add message ID to conversation history
		m.Conversations[contextID].MessageIDs = append(m.Conversations[contextID].MessageIDs, message.MessageID)
		// Update last access time
		m.Conversations[contextID].LastAccessTime = time.Now()

		// Limit history length
		if len(m.Conversations[contextID].MessageIDs) > m.options.MaxHistoryLength {
			// Remove the oldest message
			removedMsgID := m.Conversations[contextID].MessageIDs[0]
			m.Conversations[contextID].MessageIDs = m.Conversations[contextID].MessageIDs[1:]
			// Delete old message from message storage
			delete(m.Messages, removedMsgID)
		}
	}
}

// getConversationHistory gets conversation history of specified length
func (m *TaskManager) getConversationHistory(contextID string, length int) []protocol.Message {
	m.conversationMu.RLock()
	defer m.conversationMu.RUnlock()

	var history []protocol.Message

	if conversation, exists := m.Conversations[contextID]; exists {
		// Update last access time
		conversation.LastAccessTime = time.Now()

		start := 0
		if len(conversation.MessageIDs) > length {
			start = len(conversation.MessageIDs) - length
		}

		for i := start; i < len(conversation.MessageIDs); i++ {
			if msg, exists := m.Messages[conversation.MessageIDs[i]]; exists {
				history = append(history, msg)
			}
		}
	}

	return history
}

// isFinalState checks if it's a final state
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed ||
		state == protocol.TaskStateCanceled ||
		state == protocol.TaskStateRejected
}

// =============================================================================
// Configuration related types and helper methods
// =============================================================================

// processConfiguration processes and normalizes Configuration
func (m *TaskManager) processConfiguration(config *protocol.SendMessageConfiguration) taskmanager.ProcessOptions {
	result := taskmanager.ProcessOptions{
		// v1.0 default: returnImmediately=false means the request blocks until a
		// terminal/interrupted state, so a missing configuration is blocking.
		Blocking:      true,
		HistoryLength: 0,
	}

	if config == nil {
		return result
	}

	// Process Blocking configuration (v1: ReturnImmediately with inverted semantics)
	result.Blocking = config.IsBlocking()

	// Process HistoryLength configuration
	if config.HistoryLength != nil && *config.HistoryLength > 0 {
		result.HistoryLength = *config.HistoryLength
	}

	// Process PushNotificationConfig (flat TaskPushNotificationConfig -> details view)
	if config.PushConfig != nil {
		result.PushNotificationConfig = config.PushConfig.Details()
	}

	// Process AcceptedOutputModes configuration
	if config.AcceptedOutputModes != nil {
		result.AcceptedOutputModes = config.AcceptedOutputModes
	}

	return result
}

// processRequestMessage processes the request message, add messageID and contextID if not set
func (m *TaskManager) processRequestMessage(message *protocol.Message) {
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}

	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}

	m.storeMessage(*message)
}

// sendStreamingEventHook is a hook for sending streaming events
func (m *TaskManager) sendStreamingEventHook(ctxID string) func(event protocol.StreamResponse) error {
	return func(event protocol.StreamResponse) error {
		if event.GetStatusUpdate() != nil && event.GetStatusUpdate().ContextID == "" {
			event.GetStatusUpdate().ContextID = ctxID
		}
		if event.GetArtifactUpdate() != nil && event.GetArtifactUpdate().ContextID == "" {
			event.GetArtifactUpdate().ContextID = ctxID
		}
		if event.GetMessage() != nil {
			m.processReplyMessage(&ctxID, event.GetMessage())
		}
		if event.GetTask() != nil && event.GetTask().ContextID == "" {
			event.GetTask().ContextID = ctxID
		}
		return nil
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

func (m *TaskManager) checkTaskExists(taskID string) bool {
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	_, exists := m.Tasks[taskID]
	return exists
}

// notifySubscribers notifies all subscribers of the task
func (m *TaskManager) notifySubscribers(taskID string, event protocol.StreamResponse) {
	m.taskMu.RLock()
	subs, exists := m.Subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.taskMu.RUnlock()
		return
	}

	subsCopy := make([]*TaskSubscriber, len(subs))
	copy(subsCopy, subs)
	m.taskMu.RUnlock()

	log.Debugf("Notifying %d subscribers for task %s", len(subsCopy), taskID)

	var failedSubscribers []*TaskSubscriber

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
func (m *TaskManager) cleanupFailedSubscribers(taskID string, failedSubscribers []*TaskSubscriber) {
	m.taskMu.Lock()

	subs, exists := m.Subscribers[taskID]
	if !exists {
		m.taskMu.Unlock()
		return
	}

	// Filter out failed subscribers
	filteredSubs := make([]*TaskSubscriber, 0, len(subs))
	removedSubs := make([]*TaskSubscriber, 0, len(failedSubscribers))

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
		m.Subscribers[taskID] = filteredSubs
		log.Debugf("Removed %d failed subscribers for task %s", len(removedSubs), taskID)

		// If there are no subscribers left, delete the entire entry
		if len(filteredSubs) == 0 {
			delete(m.Subscribers, taskID)
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

	subs, exists := m.Subscribers[taskID]
	if !exists {
		m.taskMu.Unlock()
		return
	}
	delete(m.Subscribers, taskID)
	m.taskMu.Unlock()

	for _, sub := range subs {
		sub.Close()
	}
	log.Debugf("Cleaned subscribers for task %s", taskID)
}

// addSubscriber adds a subscriber
func (m *TaskManager) addSubscriber(taskID string, sub *TaskSubscriber) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	if _, exists := m.Subscribers[taskID]; !exists {
		m.Subscribers[taskID] = make([]*TaskSubscriber, 0)
	}
	m.Subscribers[taskID] = append(m.Subscribers[taskID], sub)
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
	for contextID, conversation := range m.Conversations {
		if now.Sub(conversation.LastAccessTime) > maxAge {
			expiredContexts = append(expiredContexts, contextID)
			expiredMessageIDs = append(expiredMessageIDs, conversation.MessageIDs...)
		}
	}

	// Delete expired conversations
	for _, contextID := range expiredContexts {
		delete(m.Conversations, contextID)
	}

	// Delete messages from expired conversations
	for _, messageID := range expiredMessageIDs {
		delete(m.Messages, messageID)
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
	subsToClose := make([]*TaskSubscriber, 0)

	for taskID, task := range m.Tasks {
		if !isFinalState(task.Task().Status.State) {
			continue
		}
		ts, err := time.Parse(time.RFC3339, task.Task().Status.Timestamp)
		if err != nil {
			// A terminal task with an unparsable/empty timestamp would never be
			// cleaned up. This is unreachable via the public API today (all writers
			// use RFC3339), but log it so a future bad-timestamp path is observable
			// instead of leaking silently.
			log.Debugf("Skipping task %s in cleanup: unparsable timestamp %q: %v",
				taskID, task.Task().Status.Timestamp, err)
			continue
		}
		if now.Sub(ts) > maxAge {
			expiredTaskIDs = append(expiredTaskIDs, taskID)
		}
	}

	for _, taskID := range expiredTaskIDs {
		if task, exists := m.Tasks[taskID]; exists {
			task.Cancel()
			delete(m.Tasks, taskID)
		}
		if subs, exists := m.Subscribers[taskID]; exists {
			subsToClose = append(subsToClose, subs...)
			delete(m.Subscribers, taskID)
		}
	}

	m.taskMu.Unlock()

	for _, sub := range subsToClose {
		sub.Close()
	}

	if len(expiredTaskIDs) > 0 {
		log.Debugf("Cleaned %d expired tasks", len(expiredTaskIDs))

		m.mu.Lock()
		for _, taskID := range expiredTaskIDs {
			delete(m.PushNotifications, taskID)
		}
		m.mu.Unlock()
	}

	return len(expiredTaskIDs)
}

// Close stops the cleanup goroutine and releases all resources.
// It cancels any in-flight (non-terminal) tasks and closes their subscribers
// without emitting a terminal event, so streaming clients observe the channel
// close directly. It is safe to call Close multiple times; it always returns nil.
func (m *TaskManager) Close() error {
	m.closeOnce.Do(func() {
		// Signal the cleanup goroutine to stop and wait for it to exit so that no
		// tick runs concurrently with the teardown below.
		close(m.stopCleanup)
		m.cleanupWg.Wait()

		m.taskMu.Lock()
		subsToClose := make([]*TaskSubscriber, 0)
		for _, subs := range m.Subscribers {
			subsToClose = append(subsToClose, subs...)
		}
		m.Subscribers = make(map[string][]*TaskSubscriber)

		for _, task := range m.Tasks {
			task.Cancel()
		}
		m.Tasks = make(map[string]*CancellableTask)
		m.taskMu.Unlock()

		// Close subscribers outside taskMu so a stuck blocking send can never
		// wedge the manager-wide lock.
		for _, sub := range subsToClose {
			sub.Close()
		}
	})
	return nil
}

// GetConversationStats gets conversation statistics
func (m *TaskManager) GetConversationStats() map[string]interface{} {
	m.conversationMu.RLock()
	defer m.conversationMu.RUnlock()

	totalConversations := len(m.Conversations)
	totalMessages := len(m.Messages)

	oldestAccess := time.Now()
	newestAccess := time.Time{}

	for _, conversation := range m.Conversations {
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
