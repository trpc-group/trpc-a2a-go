// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides a Redis-based implementation of the A2A TaskManager interface.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

const (
	// Key prefixes for Redis storage.
	messagePrefix          = "msg:"
	conversationPrefix     = "conv:"
	taskPrefix             = "task:"
	pushNotificationPrefix = "push:"
	subscriberPrefix       = "sub:"

	// Default expiration time for Redis keys (30 days).
	defaultExpiration = 1 * time.Hour

	// Default configuration values.
	defaultMaxHistoryLength         = 100
	defaultTaskSubscriberBufferSize = 10
)

// TaskManager provides a concrete, Redis-based implementation of the
// TaskManager interface. It persists messages, conversations, and tasks in Redis.
// It requires a MessageProcessor to handle the actual agent logic.
// It is safe for concurrent use.
type TaskManager struct {
	// processor is the user-provided message processor.
	processor taskmanager.MessageProcessor
	// client is the Redis client.
	client *redis.Client
	// expiration is the time after which Redis keys expire.
	expiration time.Duration

	// subMu is a mutex for the subscribers map.
	subMu sync.RWMutex
	// subscribers is a map of task IDs to subscriber channels.
	subscribers map[string][]*TaskSubscriber

	// cancelMu is a mutex for the cancels map.
	cancelMu sync.RWMutex
	// cancels is a map of task IDs to cancellation functions.
	cancels map[string]context.CancelFunc

	// options
	options *TaskManagerOptions
}

// NewTaskManager creates a new Redis-based TaskManager with the provided options.
func NewTaskManager(
	client *redis.Client,
	processor taskmanager.MessageProcessor,
	opts ...TaskManagerOption,
) (*TaskManager, error) {
	if processor == nil {
		return nil, errors.New("processor cannot be nil")
	}
	if client == nil {
		return nil, errors.New("redis client cannot be nil")
	}

	// Test connection.
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	// Apply default options
	options := DefaultRedisTaskManagerOptions()

	// Apply user options
	for _, opt := range opts {
		opt(options)
	}

	// Use expiration time from options
	expiration := options.ExpireTime

	manager := &TaskManager{
		processor:   processor,
		client:      client,
		expiration:  expiration,
		subscribers: make(map[string][]*TaskSubscriber),
		cancels:     make(map[string]context.CancelFunc),
		options:     options,
	}

	return manager, nil
}

// OnSendMessage handles the message/send request.
func (m *TaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.MessageResult, error) {
	log.Debugf("RedisTaskManager: OnSendMessage for message %s", request.Message.MessageID)

	// Process the request message.
	m.processRequestMessage(&request.Message)

	// Process configuration.
	options := m.processConfiguration(request.Configuration)
	options.Streaming = false // non-streaming processing

	// Create MessageHandle.
	handle := &taskHandler{
		manager:                m,
		messageID:              request.Message.MessageID,
		ctx:                    ctx,
		subscriberBufSize:      m.options.TaskSubscriberBufSize,
		subscriberBlockingSend: m.options.TaskSubscriberBlockingSend,
	}

	// Call the user's message processor.
	result, err := m.processor.ProcessMessage(ctx, request.Message, options, handle)
	if err != nil {
		return nil, fmt.Errorf("message processing failed: %w", err)
	}

	if result == nil {
		return nil, fmt.Errorf("processor returned nil result")
	}

	// Check if the user returned StreamingEvents for non-streaming request.
	if result.StreamingEvents != nil {
		log.Infof("User returned StreamingEvents for non-streaming request, ignoring")
	}

	if result.Result == nil {
		return nil, fmt.Errorf("processor returned nil result for non-streaming request")
	}

	switch result.Result.(type) {
	case *protocol.Task:
	case *protocol.Message:
	default:
		return nil, fmt.Errorf("processor returned unsupported result type %T for SendMessage request", result.Result)
	}

	if message, ok := result.Result.(*protocol.Message); ok {
		var contextID string
		if request.Message.ContextID != nil {
			contextID = *request.Message.ContextID
		}
		m.processReplyMessage(&contextID, message)
	}

	return &protocol.MessageResult{Result: result.Result}, nil
}

// OnSendMessageStream handles message/stream requests.
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	log.Debugf("RedisTaskManager: OnSendMessageStream for message %s", request.Message.MessageID)

	m.processRequestMessage(&request.Message)

	// Process configuration.
	options := m.processConfiguration(request.Configuration)
	options.Streaming = true // streaming mode

	// Create streaming MessageHandle.
	handle := &taskHandler{
		manager:                m,
		messageID:              request.Message.MessageID,
		ctx:                    ctx,
		subscriberBufSize:      m.options.TaskSubscriberBufSize,
		subscriberBlockingSend: m.options.TaskSubscriberBlockingSend,
	}

	// Call user's message processor.
	result, err := m.processor.ProcessMessage(ctx, request.Message, options, handle)
	if err != nil {
		return nil, fmt.Errorf("message processing failed: %w", err)
	}

	if result == nil || result.StreamingEvents == nil {
		return nil, fmt.Errorf("processor returned nil result")
	}

	return result.StreamingEvents.Channel(), nil
}

// OnGetTask handles the tasks/get request.
func (m *TaskManager) OnGetTask(
	ctx context.Context,
	params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// If the request contains history length, fill the message history.
	if params.HistoryLength != nil && *params.HistoryLength > 0 {
		if task.ContextID != "" {
			history, err := m.getConversationHistory(ctx, task.ContextID, *params.HistoryLength)
			if err != nil {
				log.Warnf("Failed to retrieve message history for task %s: %v", params.ID, err)
				// Continue without history rather than failing the whole request.
			} else {
				task.History = history
			}
		}
	}

	return task, nil
}

// OnCancelTask handles the tasks/cancel request.
func (m *TaskManager) OnCancelTask(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// Check if task is already in a final state.
	if isFinalState(task.Status.State) {
		return task, fmt.Errorf("task %s is already in final state: %s", params.ID, task.Status.State)
	}

	var cancelFound bool
	m.cancelMu.Lock()
	cancel, exists := m.cancels[params.ID]
	if exists {
		cancel() // Call the cancel function.
		cancelFound = true
		// Don't delete the context here - let the processor goroutine clean up.
	}
	m.cancelMu.Unlock()

	// If no cancellation function was found, log a warning.
	if !cancelFound {
		log.Warnf("Warning: No cancellation function found for task %s", params.ID)
	}

	// Update task state to Cancelled.
	task.Status.State = protocol.TaskStateCanceled
	task.Status.Timestamp = time.Now().UTC().Format(time.RFC3339)

	// Store updated task.
	if err := m.storeTask(ctx, task); err != nil {
		log.Errorf("Error storing cancelled task %s: %v", params.ID, err)
		return nil, err
	}

	// Clean up subscribers.
	m.cleanSubscribers(params.ID)

	return task, nil
}

// OnPushNotificationSet handles tasks/pushNotificationConfig/set requests.
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	// Check if task exists.
	_, err := m.getTaskInternal(ctx, params.TaskID)
	if err != nil {
		return nil, err
	}

	// Store the push notification configuration.
	pushKey := pushNotificationPrefix + params.TaskID
	configBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize push notification config: %w", err)
	}

	if err := m.client.Set(ctx, pushKey, configBytes, m.expiration).Err(); err != nil {
		return nil, fmt.Errorf("failed to store push notification config: %w", err)
	}

	log.Debugf("RedisTaskManager: Push notification config set for task %s", params.TaskID)
	return &params, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests.
func (m *TaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	// Check if task exists.
	_, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// Retrieve the push notification configuration.
	pushKey := pushNotificationPrefix + params.ID
	configBytes, err := m.client.Get(ctx, pushKey).Bytes()
	if err != nil {
		return nil, fmt.Errorf("push notification config not found for task: %s", params.ID)
	}

	var config protocol.TaskPushNotificationConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
	}

	return &config, nil
}

// OnResubscribe handles tasks/resubscribe requests.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	// Check if task exists.
	_, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	bufSize := m.options.TaskSubscriberBufSize
	if bufSize <= 0 {
		bufSize = defaultTaskSubscriberBufferSize
	}

	subscriber := NewTaskSubscriber(
		params.ID,
		bufSize,
		WithSubscriberBlockingSend(m.options.TaskSubscriberBlockingSend),
		WithSubscriberSendHook(m.sendStreamingEventHook(params.ID)),
	)

	// Add to subscribers list.
	m.addSubscriber(params.ID, subscriber)

	return subscriber.Channel(), nil
}

// OnSendTask deprecated method empty implementation.
func (m *TaskManager) OnSendTask(
	ctx context.Context,
	request protocol.SendTaskParams,
) (*protocol.Task, error) {
	return nil, fmt.Errorf("OnSendTask is deprecated, use OnSendMessage instead")
}

// OnSendTaskSubscribe deprecated method empty implementation.
func (m *TaskManager) OnSendTaskSubscribe(
	ctx context.Context,
	request protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	return nil, fmt.Errorf("OnSendTaskSubscribe is deprecated, use OnSendMessageStream instead")
}

// =============================================================================
// Internal helper methods
// =============================================================================

// processConfiguration processes and normalizes configuration.
func (m *TaskManager) processConfiguration(
	config *protocol.SendMessageConfiguration,
) taskmanager.ProcessOptions {
	result := taskmanager.ProcessOptions{
		Blocking:      false,
		HistoryLength: 0,
	}

	if config == nil {
		return result
	}

	// Process Blocking configuration.
	if config.Blocking != nil {
		result.Blocking = *config.Blocking
	}

	// Process HistoryLength configuration.
	if config.HistoryLength != nil && *config.HistoryLength > 0 {
		result.HistoryLength = *config.HistoryLength
	}

	// Process PushNotificationConfig.
	if config.PushNotificationConfig != nil {
		result.PushNotificationConfig = config.PushNotificationConfig
	}

	return result
}

// processRequestMessage processes and stores the request message.
func (m *TaskManager) processRequestMessage(message *protocol.Message) {
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}

	if message.ContextID == nil {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}

	m.storeMessage(context.Background(), *message)
}

// processReplyMessage processes and stores the reply message.
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

	m.storeMessage(context.Background(), *message)
}

// sendStreamingEventHook is a hook for sending streaming events
// used to set contextID for task status update, task artifact update, message and task events
func (m *TaskManager) sendStreamingEventHook(ctxID string) func(event protocol.StreamingMessageEvent) error {
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

// storeMessage stores a message in Redis and updates conversation history.
func (m *TaskManager) storeMessage(ctx context.Context, message protocol.Message) {
	// Store the message.
	msgKey := messagePrefix + message.MessageID
	msgBytes, err := json.Marshal(message)
	if err != nil {
		log.Errorf("Failed to serialize message %s: %v", message.MessageID, err)
		return
	}

	if err := m.client.Set(ctx, msgKey, msgBytes, m.expiration).Err(); err != nil {
		log.Errorf("Failed to store message %s in Redis: %v", message.MessageID, err)
		return
	}

	// If the message has a contextID, add it to conversation history.
	if message.ContextID != nil {
		contextID := *message.ContextID
		convKey := conversationPrefix + contextID

		// Add message ID to conversation history using Redis list.
		if err := m.client.RPush(ctx, convKey, message.MessageID).Err(); err != nil {
			log.Errorf("Failed to add message %s to conversation %s: %v", message.MessageID, contextID, err)
			return
		}

		// Set expiration on the conversation list.
		m.client.Expire(ctx, convKey, m.expiration).Err()

		// Limit history length by trimming the list.
		if err := m.client.LTrim(ctx, convKey, -int64(m.options.MaxHistoryLength), -1).Err(); err != nil {
			log.Errorf("Failed to trim conversation %s: %v", contextID, err)
		}
	}
}

// getConversationHistory retrieves conversation history for a context.
func (m *TaskManager) getConversationHistory(
	ctx context.Context,
	contextID string,
	length int,
) ([]protocol.Message, error) {
	if contextID == "" {
		return nil, nil
	}

	convKey := conversationPrefix + contextID

	// Get the message count.
	count, err := m.client.LLen(ctx, convKey).Result()
	if err != nil {
		return nil, nil // No messages found.
	}

	// Calculate range for LRANGE (get the latest messages).
	start := int64(0)
	if count > int64(length) {
		start = count - int64(length)
	}

	// Get message IDs.
	messageIDs, err := m.client.LRange(ctx, convKey, start, count-1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve message IDs: %w", err)
	}

	// Retrieve messages.
	messages := make([]protocol.Message, 0, len(messageIDs))
	for _, msgID := range messageIDs {
		msgKey := messagePrefix + msgID
		msgBytes, err := m.client.Get(ctx, msgKey).Bytes()
		if err != nil {
			log.Warnf("Message %s not found in Redis", msgID)
			continue // Skip missing messages.
		}

		var msg protocol.Message
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Errorf("Failed to deserialize message %s: %v", msgID, err)
			continue // Skip invalid messages.
		}

		messages = append(messages, msg)
	}

	return messages, nil
}

// getTaskInternal retrieves a task from Redis.
func (m *TaskManager) getTaskInternal(ctx context.Context, taskID string) (*protocol.Task, error) {
	taskKey := taskPrefix + taskID
	taskBytes, err := m.client.Get(ctx, taskKey).Bytes()
	if err != nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	var task protocol.Task
	if err := json.Unmarshal(taskBytes, &task); err != nil {
		return nil, fmt.Errorf("failed to deserialize task: %w", err)
	}

	return &task, nil
}

// storeTask stores a task in Redis.
func (m *TaskManager) storeTask(ctx context.Context, task *protocol.Task) error {
	taskKey := taskPrefix + task.ID
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	if err := m.client.Set(ctx, taskKey, taskBytes, m.expiration).Err(); err != nil {
		return fmt.Errorf("failed to store task: %w", err)
	}

	return nil
}

// deleteTask deletes a task from Redis.
func (m *TaskManager) deleteTask(ctx context.Context, taskID string) error {
	taskKey := taskPrefix + taskID
	if err := m.client.Del(ctx, taskKey).Err(); err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	return nil
}

// isFinalState checks if a TaskState represents a terminal state.
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed ||
		state == protocol.TaskStateCanceled ||
		state == protocol.TaskStateRejected
}

// addSubscriber adds a subscriber to the list.
func (m *TaskManager) addSubscriber(taskID string, sub *TaskSubscriber) {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	if _, exists := m.subscribers[taskID]; !exists {
		m.subscribers[taskID] = make([]*TaskSubscriber, 0)
	}
	m.subscribers[taskID] = append(m.subscribers[taskID], sub)
	log.Debugf("Added subscriber for task %s", taskID)
}

// cleanSubscribers cleans up all subscribers for a task.
func (m *TaskManager) cleanSubscribers(taskID string) {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	if subs, exists := m.subscribers[taskID]; exists {
		for _, sub := range subs {
			sub.Close()
		}
		delete(m.subscribers, taskID)
		log.Debugf("Cleaned subscribers for task %s", taskID)
	}
}

// notifySubscribers notifies all subscribers of a task.
func (m *TaskManager) notifySubscribers(taskID string, event protocol.StreamingMessageEvent) {
	m.subMu.RLock()
	subs, exists := m.subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.subMu.RUnlock()
		return
	}

	subsCopy := make([]*TaskSubscriber, len(subs))
	copy(subsCopy, subs)
	m.subMu.RUnlock()

	log.Debugf("Notifying %d subscribers for task %s (Event Type: %T)", len(subsCopy), taskID, event.Result)

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

	// Clean up failed or closed subscribers.
	if len(failedSubscribers) > 0 {
		m.cleanupFailedSubscribers(taskID, failedSubscribers)
	}
}

// cleanupFailedSubscribers cleans up failed or closed subscribers.
func (m *TaskManager) cleanupFailedSubscribers(taskID string, failedSubscribers []*TaskSubscriber) {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	subs, exists := m.subscribers[taskID]
	if !exists {
		return
	}

	// Filter out failed subscribers.
	filteredSubs := make([]*TaskSubscriber, 0, len(subs))
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
		m.subscribers[taskID] = filteredSubs
		log.Debugf("Removed %d failed subscribers for task %s", removedCount, taskID)

		// If there are no subscribers left, delete the entire entry.
		if len(filteredSubs) == 0 {
			delete(m.subscribers, taskID)
		}
	}
}

// Close closes the Redis client and cleans up resources.
func (m *TaskManager) Close() error {
	// Cancel all active contexts.
	m.cancelMu.Lock()
	for _, cancel := range m.cancels {
		cancel()
	}
	m.cancels = make(map[string]context.CancelFunc)
	m.cancelMu.Unlock()

	// Close all subscriber channels.
	m.subMu.Lock()
	for _, subscribers := range m.subscribers {
		for _, sub := range subscribers {
			sub.Close()
		}
	}
	m.subscribers = make(map[string][]*TaskSubscriber)
	m.subMu.Unlock()

	// Close the Redis client.
	return m.client.Close()
}
