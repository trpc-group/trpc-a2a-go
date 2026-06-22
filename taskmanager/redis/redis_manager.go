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
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const (
	// Key prefixes for Redis storage.
	messagePrefix          = "msg:"
	conversationPrefix     = "conv:"
	taskPrefix             = "task:"
	pushNotificationPrefix = "push:"
	subscriberPrefix       = "sub:"

	// Default expiration time for Redis keys (1 hour).
	defaultExpiration = 1 * time.Hour

	// Default configuration values.
	defaultMaxHistoryLength         = 100
	defaultTaskSubscriberBufferSize = 1024
)

// TaskManager provides a concrete, Redis-based implementation of the
// TaskManager interface. It persists messages, conversations, and tasks in Redis.
// It requires a MessageProcessor to handle the actual agent logic.
// It is safe for concurrent use.
type TaskManager struct {
	// processor is the user-provided message processor.
	processor taskmanager.MessageProcessor
	// client is the Redis client.
	client redis.UniversalClient
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
	client redis.UniversalClient,
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
) (*protocol.SendMessageResponse, error) {
	log.Debugf("RedisTaskManager: OnSendMessage for message %s", request.Message.MessageID)

	// Process the request message.
	m.processRequestMessage(&request.Message)

	// Process configuration.
	options := m.processConfiguration(request.Configuration)
	options.Streaming = false // non-streaming processing
	options.Tenant = request.Tenant

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

	if result.Result.Message != nil {
		var contextID string
		if request.Message.ContextID != nil {
			contextID = *request.Message.ContextID
		}
		m.processReplyMessage(&contextID, result.Result.Message)
	}

	return result.Result, nil
}

// OnSendMessageStream handles message/stream requests.
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	log.Debugf("RedisTaskManager: OnSendMessageStream for message %s", request.Message.MessageID)

	m.processRequestMessage(&request.Message)

	// Process configuration.
	options := m.processConfiguration(request.Configuration)
	options.Streaming = true // streaming mode
	options.Tenant = request.Tenant

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

	// Fill message history per the v1.0 GetTaskRequest semantics:
	//   - historyLength unset -> no limit (full history)
	//   - historyLength == 0  -> no messages
	//   - historyLength > 0   -> the most recent N messages
	if task.ContextID != "" {
		length := -1 // sentinel: unset -> unlimited
		switch {
		case params.HistoryLength == nil:
			length = unlimitedHistoryLength
		case *params.HistoryLength > 0:
			length = *params.HistoryLength
		default: // == 0 (or negative): no messages
			task.History = nil
		}
		if length >= 0 {
			history, err := m.getConversationHistory(ctx, task.ContextID, length)
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
		return nil, taskmanager.ErrTaskNotCancelable(params.ID, task.Status.State)
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

// unlimitedHistoryLength is used to request the full conversation history when
// a v1.0 request leaves historyLength unset (spec: unset means no limit).
const unlimitedHistoryLength = 1 << 30

// OnListTasks handles the v1.0 ListTasks request by scanning stored tasks,
// filtering, and applying offset-based pagination.
func (m *TaskManager) OnListTasks(
	ctx context.Context,
	params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	afterTime, err := taskmanager.ParseListTasksStatusTimestampAfter(params.StatusTimestampAfter)
	if err != nil {
		return nil, err
	}

	// Scan all task keys and collect matching tasks.
	var filtered []*protocol.Task
	iter := m.client.Scan(ctx, 0, taskPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		taskBytes, err := m.client.Get(ctx, iter.Val()).Bytes()
		if err != nil {
			continue // Key expired between SCAN and GET.
		}
		var task protocol.Task
		if err := json.Unmarshal(taskBytes, &task); err != nil {
			log.Errorf("RedisTaskManager: skip malformed task at %s: %v", iter.Val(), err)
			continue
		}
		if taskmanager.TaskMatchesListFilter(&task, params, afterTime) {
			filtered = append(filtered, &task)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan tasks: %w", err)
	}
	return taskmanager.PaginateTasks(filtered, params)
}

// OnPushNotificationList handles the v1.0 ListTaskPushNotificationConfigs request.
// The Redis manager stores at most one configuration per task, so the result
// contains zero or one entries.
func (m *TaskManager) OnPushNotificationList(
	ctx context.Context,
	params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	// Check if task exists.
	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return nil, err
	}

	result := &protocol.ListTaskPushNotificationConfigsResult{
		Configs: []protocol.TaskPushNotificationConfig{},
	}
	configBytes, err := m.client.Get(ctx, pushNotificationPrefix+params.TaskID).Bytes()
	if err != nil {
		return result, nil // No config registered.
	}
	var config protocol.TaskPushNotificationConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
	}
	result.Configs = append(result.Configs, config)
	return result, nil
}

// OnPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig request.
// Deleting a non-existent configuration is a no-op.
func (m *TaskManager) OnPushNotificationDelete(
	ctx context.Context,
	params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	if err := m.client.Del(ctx, pushNotificationPrefix+params.TaskID).Err(); err != nil {
		return fmt.Errorf("failed to delete push notification config: %w", err)
	}
	log.Debugf("RedisTaskManager: Push notification config deleted for task %s", params.TaskID)
	return nil
}

// OnResubscribe handles tasks/resubscribe requests.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	// Check if task exists.
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// v1.0: a subscription is only valid for a non-terminal task; a task already
	// in a terminal state must be rejected with UnsupportedOperationError.
	if isFinalState(task.Status.State) {
		return nil, taskmanager.ErrUnsupportedOperation(
			fmt.Sprintf("subscribe to task %s in terminal state %s", params.ID, task.Status.State))
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

	// v1.0: the first stream event must be the current Task snapshot. getTaskInternal
	// already returns a fresh copy, so it is safe to hand to the subscriber.
	if err := subscriber.Send(protocol.StreamResponse{Task: task}); err != nil {
		subscriber.Close()
		return nil, err
	}

	// Add to subscribers list.
	m.addSubscriber(params.ID, subscriber)

	return subscriber.Channel(), nil
}

// =============================================================================
// Internal helper methods
// =============================================================================

// processConfiguration processes and normalizes configuration.
func (m *TaskManager) processConfiguration(
	config *protocol.SendMessageConfiguration,
) taskmanager.ProcessOptions {
	result := taskmanager.ProcessOptions{
		// v1.0 default: returnImmediately=false means the request blocks until a
		// terminal/interrupted state, so a missing configuration is blocking.
		Blocking:      true,
		HistoryLength: 0,
	}

	if config == nil {
		return result
	}

	// Process Blocking configuration (v1: ReturnImmediately with inverted semantics).
	result.Blocking = config.IsBlocking()

	// Process HistoryLength configuration.
	if config.HistoryLength != nil && *config.HistoryLength > 0 {
		result.HistoryLength = *config.HistoryLength
	}

	// Process PushNotificationConfig (flat TaskPushNotificationConfig -> details view).
	if config.PushConfig != nil {
		result.PushNotificationConfig = config.PushConfig.Details()
	}

	// Process AcceptedOutputModes configuration.
	if config.AcceptedOutputModes != nil {
		result.AcceptedOutputModes = config.AcceptedOutputModes
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
func (m *TaskManager) sendStreamingEventHook(ctxID string) func(event protocol.StreamResponse) error {
	return func(event protocol.StreamResponse) error {
		if event.StatusUpdate != nil && event.StatusUpdate.ContextID == "" {
			event.StatusUpdate.ContextID = ctxID
		}
		if event.ArtifactUpdate != nil && event.ArtifactUpdate.ContextID == "" {
			event.ArtifactUpdate.ContextID = ctxID
		}
		if event.Message != nil {
			m.processReplyMessage(&ctxID, event.Message)
		}
		if event.Task != nil && event.Task.ContextID == "" {
			event.Task.ContextID = ctxID
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
		return nil, taskmanager.ErrTaskNotFound(taskID)
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
func (m *TaskManager) notifySubscribers(taskID string, event protocol.StreamResponse) {
	m.subMu.RLock()
	subs, exists := m.subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.subMu.RUnlock()
		return
	}

	subsCopy := make([]*TaskSubscriber, len(subs))
	copy(subsCopy, subs)
	m.subMu.RUnlock()

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
