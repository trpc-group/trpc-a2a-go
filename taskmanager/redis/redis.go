// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
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

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

const (
	// Key prefixes for Redis storage.
	taskPrefix             = "task:"
	messagePrefix          = "msg:"
	pushNotificationPrefix = "push:"
	subscriberPrefix       = "sub:"

	// Default expiration time for Redis keys (30 days).
	defaultExpiration = 30 * 24 * time.Hour
)

// TaskManager provides a concrete, Redis-based implementation of the
// TaskManager interface. It persists tasks and messages in Redis.
// It requires a TaskProcessor to handle the actual agent logic.
// It is safe for concurrent use.
type TaskManager struct {
	// processor is the agent logic processor.
	processor taskmanager.TaskProcessor
	// client is the Redis client.
	client redis.UniversalClient
	// expiration is the time after which Redis keys expire.
	expiration time.Duration

	// subMu is a mutex for the Subscribers map.
	subMu sync.RWMutex
	// subscribers is a map of task IDs to subscriber channels.
	subscribers map[string][]chan<- protocol.TaskEvent

	// cancelMu is a mutex for the cancels map.
	cancelMu sync.RWMutex
	// cancels is a map of task IDs to cancellation functions.
	cancels map[string]context.CancelFunc

	// pushAuth is the push notification authenticator.
	pushAuth *auth.PushNotificationAuthenticator
	// pushAuthMu is a mutex for the pushAuth field.
	pushAuthMu sync.Mutex
}

// NewRedisTaskManager creates a new Redis-based TaskManager with the provided options.
func NewRedisTaskManager(
	client redis.UniversalClient,
	processor taskmanager.TaskProcessor,
	opts ...Option,
) (*TaskManager, error) {
	if processor == nil {
		return nil, errors.New("task processor cannot be nil")
	}
	// Test connection.
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}
	expiration := defaultExpiration
	manager := &TaskManager{
		processor:   processor,
		client:      client,
		expiration:  expiration,
		subscribers: make(map[string][]chan<- protocol.TaskEvent),
		cancels:     make(map[string]context.CancelFunc),
	}
	for _, opt := range opts {
		opt(manager)
	}
	return manager, nil
}

// redisTaskHandle implements the TaskHandle interface for Redis.
type redisTaskHandle struct {
	taskID  string
	manager *TaskManager
}

// UpdateStatus implements TaskHandle.
func (h *redisTaskHandle) UpdateStatus(state protocol.TaskState, msg *protocol.Message) error {
	return h.manager.UpdateTaskStatus(h.taskID, state, msg)
}

// AddArtifact implements TaskHandle
func (h *redisTaskHandle) AddArtifact(artifact protocol.Artifact) error {
	return h.manager.AddArtifact(h.taskID, artifact)
}

// IsStreamingRequest implements TaskHandle.
// It returns true if there are active subscribers for this task,
// indicating it was initiated with OnSendTaskSubscribe rather than OnSendTask.
func (h *redisTaskHandle) IsStreamingRequest() bool {
	h.manager.subMu.RLock()
	defer h.manager.subMu.RUnlock()

	subscribers, exists := h.manager.subscribers[h.taskID]
	return exists && len(subscribers) > 0
}

// GetSessionID implements TaskHandle.
func (h *redisTaskHandle) GetSessionID() *string {
	h.manager.subMu.RLock()
	defer h.manager.subMu.RUnlock()

	task, err := h.manager.getTaskInternal(context.Background(), h.taskID)
	if err != nil {
		log.Errorf("Error getting session ID for task %s: %v", h.taskID, err)
		return nil
	}

	return task.SessionID
}

// OnSendTask handles the creation or retrieval of a task and initiates synchronous processing.
func (m *TaskManager) OnSendTask(ctx context.Context, params protocol.SendTaskParams) (*protocol.Task, error) {
	// Create or update task
	_ = m.upsertTask(ctx, params)
	// Store the initial message
	m.storeMessage(ctx, params.ID, params.Message)
	// Create a cancellable context for this specific task processing
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Ensure context is cancelled eventually.
	handle := &redisTaskHandle{
		taskID:  params.ID,
		manager: m,
	}
	// Set initial status to Working *before* calling Process.
	if err := m.UpdateTaskStatus(params.ID, protocol.TaskStateWorking, nil); err != nil {
		log.Errorf("Error setting initial Working status for task %s: %v", params.ID, err)
		// Return the task state as it exists, but also the error.
		latestTask, _ := m.getTaskInternal(ctx, params.ID) // Ignore get error for now.
		return latestTask, fmt.Errorf("failed to set initial working status: %w", err)
	}
	// Delegate the actual processing to the injected processor (synchronously).
	var processorErr error
	if processorErr = m.processor.Process(taskCtx, params.ID, params.Message, handle); processorErr != nil {
		log.Errorf("Processor failed for task %s: %v", params.ID, processorErr)
		errMsg := &protocol.Message{
			Role:  protocol.MessageRoleAgent,
			Parts: []protocol.Part{protocol.NewTextPart(processorErr.Error())},
		}
		// Log update error while still handling the processor error.
		if updateErr := m.UpdateTaskStatus(params.ID, protocol.TaskStateFailed, errMsg); updateErr != nil {
			log.Errorf("Failed to update task %s status to failed: %v", params.ID, updateErr)
		}
	}
	// Return the latest task state after processing.
	finalTask, getErr := m.getTaskInternal(ctx, params.ID)
	if getErr != nil {
		// Only return an error if we couldn't retrieve the task state.
		return nil, fmt.Errorf("failed to get final task state: %w", getErr)
	}
	// Don't return the processor error, as it's already reflected in the task state.
	return finalTask, nil
}

// OnSendTaskSubscribe creates a new task and returns a channel for receiving TaskEvent updates.
func (m *TaskManager) OnSendTaskSubscribe(
	ctx context.Context,
	params protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	// Create a new task or update an existing one.
	task := m.upsertTask(ctx, params)
	// Store the message that came with the request.
	m.storeMessage(ctx, params.ID, params.Message)
	// Create event channel for this specific subscriber.
	eventChan := make(chan protocol.TaskEvent, 10) // Buffered to prevent blocking sends.
	m.addSubscriber(params.ID, eventChan)
	// Create a cancellable context for the processor.
	processorCtx, cancel := context.WithCancel(ctx)
	// Store the cancel function.
	m.cancelMu.Lock()
	m.cancels[params.ID] = cancel
	m.cancelMu.Unlock()
	// Set initial state if new (submitted -> working).
	// This will generate the first event for subscribers.
	if task.Status.State == protocol.TaskStateSubmitted {
		if err := m.UpdateTaskStatus(params.ID, protocol.TaskStateWorking, nil); err != nil {
			m.removeSubscriber(params.ID, eventChan)
			close(eventChan)
			return nil, err
		}
	}
	// Start the processor in a goroutine.
	go func() {
		// Create a handle for the processor to interact with the task.
		handle := &redisTaskHandle{
			taskID:  params.ID,
			manager: m,
		}
		log.Debugf("SSE Processor started for task %s", params.ID)
		var err error
		if err = m.processor.Process(processorCtx, params.ID, params.Message, handle); err != nil {
			log.Errorf("Processor failed for task %s in subscribe: %v", params.ID, err)
			if processorCtx.Err() != context.Canceled {
				// Only update to failed if not already cancelled.
				errMsg := &protocol.Message{
					Role:  protocol.MessageRoleAgent,
					Parts: []protocol.Part{protocol.NewTextPart(err.Error())},
				}
				if updateErr := m.UpdateTaskStatus(
					params.ID,
					protocol.TaskStateFailed,
					errMsg,
				); updateErr != nil {
					log.Errorf("Failed to update task %s status to failed: %v", params.ID, updateErr)
				}
			}
		}
		// Clean up the context regardless of how we finish.
		m.cancelMu.Lock()
		delete(m.cancels, params.ID)
		m.cancelMu.Unlock()
		log.Debugf("Processor finished for task %s in subscribe (Error: %v). Goroutine exiting.", params.ID, err)
		// Close event channel and clean up subscriber.
		log.Debugf("Closing event channel and removing subscriber for task %s.", params.ID)
		m.removeSubscriber(params.ID, eventChan)
		close(eventChan)
	}()
	// Return the channel for events.
	return eventChan, nil
}

// OnGetTask retrieves the current state of a task.
func (m *TaskManager) OnGetTask(
	ctx context.Context,
	params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	// Optionally include message history if requested.
	if params.HistoryLength != nil && *params.HistoryLength > 0 {
		history, err := m.getMessageHistory(ctx, params.ID, *params.HistoryLength)
		if err != nil {
			log.Warnf("Failed to retrieve message history for task %s: %v", params.ID, err)
			// Continue without history rather than failing the whole request
		} else {
			task.History = history
		}
	}
	return task, nil
}

// OnCancelTask requests the cancellation of an ongoing task.
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
		return task, taskmanager.ErrTaskFinalState(params.ID, task.Status.State)
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
	// Create a cancellation message.
	cancelMsg := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Task %s was canceled by user request", params.ID)),
		},
	}
	// Update state to Cancelled.
	if err := m.UpdateTaskStatus(params.ID, protocol.TaskStateCanceled, cancelMsg); err != nil {
		log.Errorf("Error updating status to Cancelled for task %s: %v", params.ID, err)
		return nil, err
	}
	// Fetch the updated task state to return.
	updatedTask, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task %s after cancellation update: %w", params.ID, err)
	}

	return updatedTask, nil
}

// OnPushNotificationSet configures push notifications for a specific task
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	// Check if task exists.
	_, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	// Store the push notification configuration.
	pushKey := pushNotificationPrefix + params.ID
	configBytes, err := json.Marshal(params.PushNotificationConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize push notification config: %w", err)
	}
	if err := m.client.Set(ctx, pushKey, configBytes, m.expiration).Err(); err != nil {
		return nil, fmt.Errorf("failed to store push notification config: %w", err)
	}
	log.Infof("Set push notification for task %s to URL: %s", params.ID, params.PushNotificationConfig.URL)
	// Return the stored configuration as confirmation.
	return &params, nil
}

// OnPushNotificationGet retrieves the push notification configuration for a task.
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
		if err == redis.Nil {
			return nil, taskmanager.ErrPushNotificationNotConfigured(params.ID)
		}
		return nil, fmt.Errorf("failed to retrieve push notification config: %w", err)
	}
	var config protocol.PushNotificationConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
	}
	result := &protocol.TaskPushNotificationConfig{
		ID:                     params.ID,
		PushNotificationConfig: config,
	}
	return result, nil
}

// OnResubscribe reestablishes an SSE stream for an existing task.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.TaskEvent, error) {
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	// Create a channel for events.
	eventChan := make(chan protocol.TaskEvent)
	// For tasks in final state, just send a status update event and close.
	if isFinalState(task.Status.State) {
		go func() {
			// Send a task status update event
			event := protocol.TaskStatusUpdateEvent{
				ID:     task.ID,
				Status: task.Status,
				Final:  true,
			}
			select {
			case eventChan <- event:
				// Successfully sent final status.
				log.Debugf("Sent final status to resubscribed client for task %s: %s", task.ID, task.Status.State)
			case <-ctx.Done():
				// Context was canceled.
				log.Debugf("Context done before sending status for task %s", task.ID)
			}
			// Close the channel to signal no more events.
			close(eventChan)
		}()
		return eventChan, nil
	}
	// For tasks still in progress, add this as a subscriber.
	m.addSubscriber(params.ID, eventChan)
	// Ensure we remove the subscriber when the context is canceled.
	go func() {
		<-ctx.Done()
		m.removeSubscriber(params.ID, eventChan)
		// Don't close the channel here - that should happen in the task processing goroutine.
	}()
	// Send the current status as the first event.
	go func() {
		event := protocol.TaskStatusUpdateEvent{
			ID:     task.ID,
			Status: task.Status,
			Final:  isFinalState(task.Status.State),
		}
		select {
		case eventChan <- event:
			// Successfully sent initial status.
			log.Debugf("Sent initial status to resubscribed client for task %s: %s", task.ID, task.Status.State)
		case <-ctx.Done():
			// Context was canceled.
			log.Debugf("Context done before sending initial status for task %s", task.ID)
			m.removeSubscriber(params.ID, eventChan)
			close(eventChan)
		}
	}()
	return eventChan, nil
}

// UpdateTaskStatus updates the task's state and notifies subscribers.
func (m *TaskManager) UpdateTaskStatus(
	taskID string,
	state protocol.TaskState,
	message *protocol.Message,
) error {
	ctx := context.Background()
	task, err := m.getTaskInternal(ctx, taskID)
	if err != nil {
		log.Warnf("Warning: UpdateTaskStatus called for non-existent task %s", taskID)
		return err
	}
	// Update status fields.
	task.Status = protocol.TaskStatus{
		State:     state,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	// Store updated task.
	taskKey := taskPrefix + taskID
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}
	if err := m.client.Set(ctx, taskKey, taskBytes, m.expiration).Err(); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}
	// Store the message in history if provided.
	if message != nil {
		m.storeMessage(ctx, taskID, *message)
	}
	// Notify subscribers.
	m.notifySubscribers(taskID, protocol.TaskStatusUpdateEvent{
		ID:     taskID,
		Status: task.Status,
		Final:  isFinalState(state),
	})
	return nil
}

// AddArtifact adds an artifact to the task and notifies subscribers.
func (m *TaskManager) AddArtifact(taskID string, artifact protocol.Artifact) error {
	ctx := context.Background()
	task, err := m.getTaskInternal(ctx, taskID)
	if err != nil {
		log.Warnf("Warning: AddArtifact called for non-existent task %s", taskID)
		return err
	}
	// Append the artifact.
	if task.Artifacts == nil {
		task.Artifacts = make([]protocol.Artifact, 0, 1)
	}
	task.Artifacts = append(task.Artifacts, artifact)
	// Store updated task
	taskKey := taskPrefix + taskID
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}
	if err := m.client.Set(ctx, taskKey, taskBytes, m.expiration).Err(); err != nil {
		return fmt.Errorf("failed to update task artifacts: %w", err)
	}
	// Notify subscribers.
	finalEvent := artifact.LastChunk != nil && *artifact.LastChunk
	m.notifySubscribers(taskID, protocol.TaskArtifactUpdateEvent{
		ID:       taskID,
		Artifact: artifact,
		Final:    finalEvent,
	})
	return nil
}

// --- Internal Helper Methods ---

// isFinalState checks if a TaskState represents a terminal state.
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed ||
		state == protocol.TaskStateCanceled
}

// getTaskInternal retrieves a task from Redis.
func (m *TaskManager) getTaskInternal(ctx context.Context, taskID string) (*protocol.Task, error) {
	taskKey := taskPrefix + taskID
	taskBytes, err := m.client.Get(ctx, taskKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, taskmanager.ErrTaskNotFound(taskID)
		}
		return nil, fmt.Errorf("failed to retrieve task from Redis: %w", err)
	}
	var task protocol.Task
	if err := json.Unmarshal(taskBytes, &task); err != nil {
		return nil, fmt.Errorf("failed to deserialize task: %w", err)
	}
	return &task, nil
}

// upsertTask creates a new task or updates metadata if it already exists.
func (m *TaskManager) upsertTask(ctx context.Context, params protocol.SendTaskParams) *protocol.Task {
	taskKey := taskPrefix + params.ID
	// Try to get existing task.
	existingTaskBytes, err := m.client.Get(ctx, taskKey).Bytes()
	var task *protocol.Task
	if err == nil {
		// Task exists, deserialize it.
		task = &protocol.Task{}
		if err := json.Unmarshal(existingTaskBytes, task); err != nil {
			log.Errorf("Failed to deserialize existing task %s: %v", params.ID, err)
			// Fall back to creating a new task.
			task = protocol.NewTask(params.ID, params.SessionID)
		}
		log.Debugf("Updating existing task %s", params.ID)
	} else if err == redis.Nil {
		// Task doesn't exist, create new one.
		task = protocol.NewTask(params.ID, params.SessionID)
		log.Infof("Created new task %s (Session: %v)", params.ID, params.SessionID)
	} else {
		// Redis error.
		log.Errorf("Redis error when retrieving task %s: %v", params.ID, err)
		// Fall back to creating a new task.
		task = protocol.NewTask(params.ID, params.SessionID)
	}
	// Update metadata if provided.
	if params.Metadata != nil {
		if task.Metadata == nil {
			task.Metadata = make(map[string]interface{})
		}
		for k, v := range params.Metadata {
			task.Metadata[k] = v
		}
	}
	// Store the task.
	taskBytes, err := json.Marshal(task)
	if err != nil {
		log.Errorf("Failed to serialize task %s: %v", params.ID, err)
		return task
	}
	if err := m.client.Set(ctx, taskKey, taskBytes, m.expiration).Err(); err != nil {
		log.Errorf("Failed to store task %s in Redis: %v", params.ID, err)
	}
	return task
}

// storeMessage adds a message to the task's history in Redis.
func (m *TaskManager) storeMessage(ctx context.Context, taskID string, message protocol.Message) {
	messagesKey := messagePrefix + taskID
	// Create a copy of the message to store.
	messageCopy := protocol.Message{
		Role:     message.Role,
		Metadata: message.Metadata,
	}
	if message.Parts != nil {
		messageCopy.Parts = make([]protocol.Part, len(message.Parts))
		copy(messageCopy.Parts, message.Parts)
	}
	// Serialize the message.
	messageBytes, err := json.Marshal(messageCopy)
	if err != nil {
		log.Errorf("Failed to serialize message for task %s: %v", taskID, err)
		return
	}
	// Add the message to a Redis list.
	if err := m.client.RPush(ctx, messagesKey, messageBytes).Err(); err != nil {
		log.Errorf("Failed to store message for task %s in Redis: %v", taskID, err)
		return
	}
	// Set expiration on the message list.
	m.client.Expire(ctx, messagesKey, m.expiration)
}

// getMessageHistory retrieves message history for a task.
func (m *TaskManager) getMessageHistory(
	ctx context.Context,
	taskID string,
	limit int,
) ([]protocol.Message, error) {
	messagesKey := messagePrefix + taskID
	// Get the message count.
	count, err := m.client.LLen(ctx, messagesKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // No messages found.
		}
		return nil, fmt.Errorf("failed to get message count: %w", err)
	}
	// Calculate range for LRANGE (get the latest messages).
	start := int64(0)
	if count > int64(limit) {
		start = count - int64(limit)
	}
	// Get messages.
	messagesBytesRaw, err := m.client.LRange(ctx, messagesKey, start, count-1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve messages: %w", err)
	}
	// Deserialize messages.
	messages := make([]protocol.Message, 0, len(messagesBytesRaw))
	for _, msgBytes := range messagesBytesRaw {
		var msg protocol.Message
		if err := json.Unmarshal([]byte(msgBytes), &msg); err != nil {
			log.Errorf("Failed to deserialize message for task %s: %v", taskID, err)
			continue // Skip invalid messages.
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// addSubscriber adds a channel to the list of subscribers for a task.
func (m *TaskManager) addSubscriber(taskID string, ch chan<- protocol.TaskEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	// If the task has no subscribers, create a new list.
	if _, exists := m.subscribers[taskID]; !exists {
		m.subscribers[taskID] = make([]chan<- protocol.TaskEvent, 0, 1)
	}
	// Add the new subscriber.
	m.subscribers[taskID] = append(m.subscribers[taskID], ch)
	log.Debugf("Added subscriber for task %s", taskID)
}

// removeSubscriber removes a specific channel from the list of subscribers for a task.
func (m *TaskManager) removeSubscriber(taskID string, ch chan<- protocol.TaskEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	channels, exists := m.subscribers[taskID]
	if !exists {
		return // No subscribers for this task.
	}
	// Filter out the channel to remove.
	var newChannels []chan<- protocol.TaskEvent
	for _, existingCh := range channels {
		if existingCh != ch {
			newChannels = append(newChannels, existingCh)
		}
	}
	// If there are no subscribers, remove the task from the map.
	if len(newChannels) == 0 {
		delete(m.subscribers, taskID)
	} else {
		m.subscribers[taskID] = newChannels
	}
	log.Debugf("Removed subscriber for task %s", taskID)
}

// notifySubscribers sends an event to all current subscribers of a task.
func (m *TaskManager) notifySubscribers(taskID string, event protocol.TaskEvent) {
	m.subMu.RLock()
	subs, exists := m.subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.subMu.RUnlock()
		return // No subscribers to notify.
	}
	// Copy the slice of channels under read lock
	subsCopy := make([]chan<- protocol.TaskEvent, len(subs))
	copy(subsCopy, subs)
	m.subMu.RUnlock()
	log.Debugf("Notifying %d subscribers for task %s (Event Type: %T, Final: %t)",
		len(subsCopy), taskID, event, event.IsFinal())
	// Send events outside the lock.
	for _, ch := range subsCopy {
		// Use a select with a default case for a non-blocking send.
		select {
		case ch <- event:
			// Event sent successfully.
		default:
			// Channel buffer is full or channel is closed.
			log.Warnf("Warning: Dropping event for task %s subscriber - channel full or closed.", taskID)
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
	for taskID, channels := range m.subscribers {
		for _, ch := range channels {
			// Try to notify of closing but don't block
			select {
			case ch <- protocol.TaskStatusUpdateEvent{
				ID: taskID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateUnknown,
					Timestamp: time.Now().UTC().Format(time.RFC3339),
				},
				Final: true,
			}:
			default:
			}
		}
	}
	m.subscribers = make(map[string][]chan<- protocol.TaskEvent)
	m.subMu.Unlock()
	// Close the Redis client.
	return m.client.Close()
}
