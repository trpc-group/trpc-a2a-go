// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package messageprocessor provides implementations for processing A2A messages.

package taskmanager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

const defaultMaxHistoryLength = 100
const defaultCleanupInterval = 30 * time.Second
const defaultConversationTTL = 1 * time.Hour
const defaultSubscriberBufferSize = 10

// ConversationHistory stores conversation history information
type ConversationHistory struct {
	// MessageIDs is the list of message IDs, ordered by time
	MessageIDs []string
	// LastAccessTime is the last access time
	LastAccessTime time.Time
}

// MemoryCancellableTask is a task that can be cancelled
type MemoryCancellableTask struct {
	task       protocol.Task
	cancelFunc context.CancelFunc
	ctx        context.Context
}

// NewCancellableTask creates a new cancellable task
func NewCancellableTask(task protocol.Task) *MemoryCancellableTask {
	cancelCtx, cancel := context.WithCancel(context.Background())
	return &MemoryCancellableTask{
		task:       task,
		cancelFunc: cancel,
		ctx:        cancelCtx,
	}
}

// Cancel cancels the task
func (t *MemoryCancellableTask) Cancel() {
	t.cancelFunc()
}

// Task returns the task
func (t *MemoryCancellableTask) Task() *protocol.Task {
	return &t.task
}

// MemoryTaskSubscriberOpts is the options for the MemoryTaskSubscriber
type MemoryTaskSubscriberOpts struct {
	sendHook     func(event protocol.StreamingMessageEvent) error
	blockingSend bool
}

// MemoryTaskSubscriberOption is the option for the MemoryTaskSubscriber
type MemoryTaskSubscriberOption func(s *MemoryTaskSubscriberOpts)

// WithSubscriberSendHook sets the send hook for the task subscriber
func WithSubscriberSendHook(hook func(event protocol.StreamingMessageEvent) error) MemoryTaskSubscriberOption {
	return func(s *MemoryTaskSubscriberOpts) {
		s.sendHook = hook
	}
}

// WithSubscriberBlockingSend sets the blocking send flag for the task subscriber
func WithSubscriberBlockingSend(blockingSend bool) MemoryTaskSubscriberOption {
	return func(s *MemoryTaskSubscriberOpts) {
		s.blockingSend = blockingSend
	}
}

// MemoryTaskSubscriber is a subscriber for a task
type MemoryTaskSubscriber struct {
	taskID         string
	eventQueue     chan protocol.StreamingMessageEvent
	lastAccessTime time.Time
	closed         atomic.Bool
	mu             sync.RWMutex
	opts           MemoryTaskSubscriberOpts
}

// NewMemoryTaskSubscriber creates a new task subscriber with specified buffer length
func NewMemoryTaskSubscriber(
	taskID string,
	bufSize int,
	opts ...MemoryTaskSubscriberOption,
) *MemoryTaskSubscriber {
	subscriberOpts := MemoryTaskSubscriberOpts{
		sendHook: nil,
	}
	for _, opt := range opts {
		opt(&subscriberOpts)
	}

	if bufSize <= 0 {
		bufSize = defaultSubscriberBufferSize // default buffer size
	}
	eventQueue := make(chan protocol.StreamingMessageEvent, bufSize)
	return &MemoryTaskSubscriber{
		taskID:         taskID,
		eventQueue:     eventQueue,
		lastAccessTime: time.Now(),
		closed:         atomic.Bool{},
		opts:           subscriberOpts,
	}
}

// Close closes the task subscriber
func (s *MemoryTaskSubscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed.Load() {
		s.closed.Store(true)
		close(s.eventQueue)
	}
}

// Channel returns the channel of the task subscriber
func (s *MemoryTaskSubscriber) Channel() <-chan protocol.StreamingMessageEvent {
	return s.eventQueue
}

// Closed returns true if the task subscriber is closed
func (s *MemoryTaskSubscriber) Closed() bool {
	return s.closed.Load()
}

// Send sends an event to the task subscriber
func (s *MemoryTaskSubscriber) Send(event protocol.StreamingMessageEvent) error {
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
		s.eventQueue <- event
		return nil
	}

	select {
	case s.eventQueue <- event:
		return nil
	default:
		return fmt.Errorf("event queue is full or closed")
	}
}

// GetLastAccessTime returns the last access time
func (s *MemoryTaskSubscriber) GetLastAccessTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccessTime
}

// MemoryTaskManager is the implementation of the MemoryTaskManager interface
type MemoryTaskManager struct {
	// mu protects the following fields
	mu sync.RWMutex

	// Processor is the user-provided message Processor
	Processor MessageProcessor

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
	Tasks map[string]*MemoryCancellableTask

	// taskMu protects the Tasks field
	taskMu sync.RWMutex

	// Subscribers stores the task subscribers
	// key: taskID, value: TaskSubscriber list
	// supports all event types: Message, Task, TaskStatusUpdateEvent, TaskArtifactUpdateEvent
	Subscribers map[string][]*MemoryTaskSubscriber

	// PushNotifications stores the push notification configurations
	// key: taskID, value: push notification configuration
	PushNotifications map[string]protocol.TaskPushNotificationConfig

	// options
	options *MemoryTaskManagerOptions
}

// NewMemoryTaskManager creates a new MemoryTaskManager instance
func NewMemoryTaskManager(processor MessageProcessor, opts ...MemoryTaskManagerOption) (*MemoryTaskManager, error) {
	if processor == nil {
		return nil, fmt.Errorf("processor cannot be nil")
	}

	// Apply default options
	options := DefaultMemoryTaskManagerOptions()

	// Apply user options
	for _, opt := range opts {
		opt(options)
	}

	manager := &MemoryTaskManager{
		Processor:         processor,
		Messages:          make(map[string]protocol.Message),
		Conversations:     make(map[string]*ConversationHistory),
		Tasks:             make(map[string]*MemoryCancellableTask),
		Subscribers:       make(map[string][]*MemoryTaskSubscriber),
		PushNotifications: make(map[string]protocol.TaskPushNotificationConfig),
		options:           options,
	}

	// Start cleanup goroutine if enabled
	if options.EnableCleanup {
		go func() {
			ticker := time.NewTicker(options.CleanupInterval)
			defer ticker.Stop()

			for range ticker.C {
				manager.CleanExpiredConversations(options.ConversationTTL)
			}
		}()
	}

	return manager, nil
}

// =============================================================================
// TaskManager interface implementation
// =============================================================================

// OnSendMessage handles the message/tasks request
func (m *MemoryTaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.MessageResult, error) {
	log.Debugf("MemoryTaskManager: OnSendMessage for message %s", request.Message.MessageID)

	// process the request message
	m.processRequestMessage(&request.Message)

	// process Configuration
	options := m.processConfiguration(request.Configuration)
	options.Streaming = false // non-streaming processing

	// create MessageHandle
	handle := &memoryTaskHandler{
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

	switch result.Result.(type) {
	case *protocol.Task:
	case *protocol.Message:
		m.processReplyMessage(request.Message.ContextID, result.Result.(*protocol.Message))
	default:
		return nil, fmt.Errorf("processor returned unsupported result type %T for SendMessage request", result.Result)
	}

	return &protocol.MessageResult{Result: result.Result}, nil
}

// OnSendMessageStream handles message/stream requests
func (m *MemoryTaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	log.Debugf("MemoryTaskManager: OnSendMessageStream for message %s", request.Message.MessageID)

	m.processRequestMessage(&request.Message)

	// Process Configuration
	options := m.processConfiguration(request.Configuration)
	options.Streaming = true // streaming mode

	// Create streaming MessageHandle
	handle := &memoryTaskHandler{
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
func (m *MemoryTaskManager) OnGetTask(ctx context.Context, params protocol.TaskQueryParams) (*protocol.Task, error) {
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()

	task, exists := m.Tasks[params.ID]
	if !exists {
		return nil, fmt.Errorf("task not found: %s", params.ID)
	}

	// return a copy of the task
	taskCopy := *task.Task()

	// if the request contains history length, fill the message history
	if params.HistoryLength != nil && *params.HistoryLength > 0 {
		if taskCopy.ContextID != "" {
			history := m.getConversationHistory(taskCopy.ContextID, *params.HistoryLength)
			taskCopy.History = history
		}
	}

	return &taskCopy, nil
}

// OnCancelTask handles the tasks/cancel request
func (m *MemoryTaskManager) OnCancelTask(ctx context.Context, params protocol.TaskIDParams) (*protocol.Task, error) {
	m.taskMu.Lock()
	task, exists := m.Tasks[params.ID]
	if !exists {
		m.taskMu.Unlock()
		return nil, fmt.Errorf("task not found: %s", params.ID)
	}

	taskCopy := *task.Task()
	m.taskMu.Unlock()

	handle := &memoryTaskHandler{
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
func (m *MemoryTaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store push notification configuration
	m.PushNotifications[params.TaskID] = params
	log.Debugf("MemoryTaskManager: Push notification config set for task %s", params.TaskID)
	return &params, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests
func (m *MemoryTaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	config, exists := m.PushNotifications[params.ID]
	if !exists {
		return nil, fmt.Errorf("push notification config not found for task: %s", params.ID)
	}

	return &config, nil
}

// OnResubscribe handles tasks/resubscribe requests
func (m *MemoryTaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	// Check if task exists
	_, exists := m.Tasks[params.ID]
	if !exists {
		return nil, fmt.Errorf("task not found: %s", params.ID)
	}

	bufSize := m.options.TaskSubscriberBufSize
	if bufSize <= 0 {
		bufSize = defaultSubscriberBufferSize
	}

	subscriber := NewMemoryTaskSubscriber(
		params.ID,
		bufSize,
		WithSubscriberBlockingSend(m.options.TaskSubscriberBlockingSend),
		WithSubscriberSendHook(m.sendStreamingEventHook(params.ID)),
	)

	// Add to subscribers list
	if _, exists := m.Subscribers[params.ID]; !exists {
		m.Subscribers[params.ID] = make([]*MemoryTaskSubscriber, 0)
	}
	m.Subscribers[params.ID] = append(m.Subscribers[params.ID], subscriber)

	return subscriber.eventQueue, nil
}

// OnSendTask deprecated method empty implementation
func (m *MemoryTaskManager) OnSendTask(ctx context.Context, request protocol.SendTaskParams) (*protocol.Task, error) {
	return nil, fmt.Errorf("OnSendTask is deprecated, use OnSendMessage instead")
}

// OnSendTaskSubscribe deprecated method empty implementation
func (m *MemoryTaskManager) OnSendTaskSubscribe(ctx context.Context, request protocol.SendTaskParams) (<-chan protocol.TaskEvent, error) {
	return nil, fmt.Errorf("OnSendTaskSubscribe is deprecated, use OnSendMessageStream instead")
}

// =============================================================================
// Internal helper methods
// =============================================================================

// storeMessage stores messages
func (m *MemoryTaskManager) storeMessage(message protocol.Message) {
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

// getMessageHistory gets message history
func (m *MemoryTaskManager) getMessageHistory(contextID string) []protocol.Message {
	var history []protocol.Message
	if contextID == "" {
		return history
	}

	// Need to protect access to both conversations and messages
	m.mu.Lock()
	defer m.mu.Unlock()

	if conversation, exists := m.Conversations[contextID]; exists {
		// Update last access time
		conversation.LastAccessTime = time.Now()

		history = make([]protocol.Message, 0, len(conversation.MessageIDs))
		for _, msgID := range conversation.MessageIDs {
			if msg, exists := m.Messages[msgID]; exists {
				history = append(history, msg)
			}
		}
	}
	return history
}

// getConversationHistory gets conversation history of specified length
func (m *MemoryTaskManager) getConversationHistory(contextID string, length int) []protocol.Message {
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
func (m *MemoryTaskManager) processConfiguration(config *protocol.SendMessageConfiguration) ProcessOptions {
	result := ProcessOptions{
		Blocking:      false,
		HistoryLength: 0,
	}

	if config == nil {
		return result
	}

	// Process Blocking configuration
	if config.Blocking != nil {
		result.Blocking = *config.Blocking
	}

	// Process HistoryLength configuration
	if config.HistoryLength != nil && *config.HistoryLength > 0 {
		result.HistoryLength = *config.HistoryLength
	}

	// Process PushNotificationConfig
	if config.PushNotificationConfig != nil {
		result.PushNotificationConfig = config.PushNotificationConfig
	}

	// Process AcceptedOutputModes configuration
	if config.AcceptedOutputModes != nil {
		result.AcceptedOutputModes = config.AcceptedOutputModes
	}

	return result
}

// processRequestMessage processes the request message, add messageID and contextID if not set
func (m *MemoryTaskManager) processRequestMessage(message *protocol.Message) {
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
func (m *MemoryTaskManager) sendStreamingEventHook(ctxID string) func(event protocol.StreamingMessageEvent) error {
	return func(event protocol.StreamingMessageEvent) error {
		switch event.Result.(type) {
		case *protocol.TaskStatusUpdateEvent:
			event := event.Result.(*protocol.TaskStatusUpdateEvent)
			if event.ContextID == "" {
				event.ContextID = ctxID
			}
		case *protocol.TaskArtifactUpdateEvent:
			event := event.Result.(*protocol.TaskArtifactUpdateEvent)
			if event.ContextID == "" {
				event.ContextID = ctxID
			}
		case *protocol.Message:
			event := event.Result.(*protocol.Message)
			// store message
			m.processReplyMessage(&ctxID, event)
		case *protocol.Task:
			event := event.Result.(*protocol.Task)
			if event.ContextID == "" {
				event.ContextID = ctxID
			}
		}
		return nil
	}
}

// processReplyMessage processes the reply message, add messageID and contextID if not set
func (m *MemoryTaskManager) processReplyMessage(ctxID *string, message *protocol.Message) {
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

func (m *MemoryTaskManager) checkTaskExists(taskID string) bool {
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	_, exists := m.Tasks[taskID]
	return exists
}

func (m *MemoryTaskManager) getTask(taskID string) (*MemoryCancellableTask, error) {
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	task, exists := m.Tasks[taskID]
	if !exists {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	return task, nil
}

// notifySubscribers notifies all subscribers of the task
func (m *MemoryTaskManager) notifySubscribers(taskID string, event protocol.StreamingMessageEvent) {
	m.taskMu.RLock()
	subs, exists := m.Subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.taskMu.RUnlock()
		return
	}

	subsCopy := make([]*MemoryTaskSubscriber, len(subs))
	copy(subsCopy, subs)
	m.taskMu.RUnlock()

	log.Debugf("Notifying %d subscribers for task %s (Event Type: %T)", len(subsCopy), taskID, event.Result)

	var failedSubscribers []*MemoryTaskSubscriber

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
func (m *MemoryTaskManager) cleanupFailedSubscribers(taskID string, failedSubscribers []*MemoryTaskSubscriber) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	subs, exists := m.Subscribers[taskID]
	if !exists {
		return
	}

	// Filter out failed subscribers
	filteredSubs := make([]*MemoryTaskSubscriber, 0, len(subs))
	removedCount := 0

	for _, sub := range subs {
		shouldRemove := false
		for _, failedSub := range failedSubscribers {
			if sub == failedSub {
				shouldRemove = true
				removedCount++
				break
			}
		}
		if !shouldRemove {
			filteredSubs = append(filteredSubs, sub)
		}
	}

	if removedCount > 0 {
		m.Subscribers[taskID] = filteredSubs
		log.Debugf("Removed %d failed subscribers for task %s", removedCount, taskID)

		// If there are no subscribers left, delete the entire entry
		if len(filteredSubs) == 0 {
			delete(m.Subscribers, taskID)
		}
	}
}

// addSubscriber adds a subscriber
func (m *MemoryTaskManager) addSubscriber(taskID string, sub *MemoryTaskSubscriber) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	if _, exists := m.Subscribers[taskID]; !exists {
		m.Subscribers[taskID] = make([]*MemoryTaskSubscriber, 0)
	}
	m.Subscribers[taskID] = append(m.Subscribers[taskID], sub)
}

// cleanSubscribers cleans up subscribers
func (m *MemoryTaskManager) cleanSubscribers(taskID string) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	for _, sub := range m.Subscribers[taskID] {
		sub.Close()
	}
	delete(m.Subscribers, taskID)
}

// CleanExpiredConversations cleans up expired conversation history
// maxAge: the maximum lifetime of the conversation, conversations not accessed beyond this time will be cleaned up
func (m *MemoryTaskManager) CleanExpiredConversations(maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

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

// GetConversationStats gets conversation statistics
func (m *MemoryTaskManager) GetConversationStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

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
