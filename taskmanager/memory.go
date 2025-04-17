// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager provides task management interfaces, types, and implementations.
package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// MemoryTaskManager provides a concrete, memory-based implementation of the
// TaskManager interface. It manages tasks, messages, and subscribers in memory.
// It requires a TaskProcessor to handle the actual agent logic.
// It is safe for concurrent use.
type MemoryTaskManager struct {
	// Processor is the agent logic processor.
	Processor TaskProcessor
	// Tasks is a map of task IDs to tasks.
	Tasks map[string]*protocol.Task
	// TasksMutex is a mutex for the Tasks map.
	TasksMutex sync.RWMutex
	// Messages is a map of task IDs to message history.
	Messages map[string][]protocol.Message
	// MessagesMutex is a mutex for the Messages map.
	MessagesMutex sync.RWMutex
	// Subscribers is a map of task IDs to subscriber channels.
	Subscribers map[string][]chan<- protocol.TaskEvent
	// SubMutex is a mutex for the Subscribers map.
	SubMutex sync.RWMutex
	// Contexts is a map of task IDs to cancellation functions.
	Contexts map[string]context.CancelFunc
	// ContextsMutex is a mutex for the Contexts map.
	ContextsMutex sync.RWMutex
	// PushNotifications is a map of task IDs to push notification configurations.
	PushNotifications map[string]protocol.PushNotificationConfig
	// PushNotificationsMutex is a mutex for the PushNotifications map.
	PushNotificationsMutex sync.RWMutex
}

// NewMemoryTaskManager creates a new instance with the provided TaskProcessor.
func NewMemoryTaskManager(processor TaskProcessor) (*MemoryTaskManager, error) {
	if processor == nil {
		return nil, errors.New("task processor cannot be nil")
	}
	return &MemoryTaskManager{
		Processor:         processor,
		Tasks:             make(map[string]*protocol.Task),
		Messages:          make(map[string][]protocol.Message),
		Subscribers:       make(map[string][]chan<- protocol.TaskEvent),
		Contexts:          make(map[string]context.CancelFunc),
		PushNotifications: make(map[string]protocol.PushNotificationConfig),
	}, nil
}

// processTaskWithProcessor handles the common task processing logic.
// It creates a taskHandle, sets initial status, and calls the processor.
func (m *MemoryTaskManager) processTaskWithProcessor(
	ctx context.Context,
	taskID string,
	message protocol.Message,
) error {
	handle := &memoryTaskHandle{
		taskID:  taskID,
		manager: m,
	}

	// Set initial status to Working before calling Process
	if err := m.UpdateTaskStatus(taskID, protocol.TaskStateWorking, nil); err != nil {
		log.Errorf("Error setting initial Working status for task %s: %v", taskID, err)
		return fmt.Errorf("failed to set initial working status: %w", err)
	}

	// Delegate the actual processing to the injected processor
	if err := m.Processor.Process(ctx, taskID, message, handle); err != nil {
		log.Errorf("Processor failed for task %s: %v", taskID, err)
		errMsg := &protocol.Message{
			Role:  protocol.MessageRoleAgent,
			Parts: []protocol.Part{protocol.NewTextPart(err.Error())},
		}
		// Log update error while still handling the processor error
		if updateErr := m.UpdateTaskStatus(taskID, protocol.TaskStateFailed, errMsg); updateErr != nil {
			log.Errorf("Failed to update task %s status to failed: %v", taskID, updateErr)
		}
		return err
	}

	return nil
}

// startTaskSubscribe starts processing a task in a goroutine that sends events to subscribers.
// It returns immediately, with the processing continuing asynchronously.
func (m *MemoryTaskManager) startTaskSubscribe(
	ctx context.Context,
	taskID string,
	message protocol.Message,
) {
	// Create a handle for the processor to interact with the task
	handle := &memoryTaskHandle{
		taskID:  taskID,
		manager: m,
	}

	log.Debugf("SSE Processor started for task %s", taskID)

	// Start the processor in a goroutine
	go func() {
		var err error
		if err = m.Processor.Process(ctx, taskID, message, handle); err != nil {
			log.Errorf("Processor failed for task %s in subscribe: %v", taskID, err)
			if ctx.Err() != context.Canceled {
				// Only update to failed if not already cancelled
				errMsg := &protocol.Message{
					Role:  protocol.MessageRoleAgent,
					Parts: []protocol.Part{protocol.NewTextPart(err.Error())},
				}
				if updateErr := m.UpdateTaskStatus(taskID, protocol.TaskStateFailed, errMsg); updateErr != nil {
					log.Errorf("Failed to update task %s status to failed: %v", taskID, updateErr)
				}
			}
		}

		// Clean up the context regardless of how we finish
		m.ContextsMutex.Lock()
		delete(m.Contexts, taskID)
		m.ContextsMutex.Unlock()

		log.Debugf("Processor finished for task %s in subscribe (Error: %v). Goroutine exiting.", taskID, err)
	}()
}

// OnSendTask handles the creation or retrieval of a task and initiates synchronous processing.
// It implements the TaskManager interface.
func (m *MemoryTaskManager) OnSendTask(ctx context.Context, params protocol.SendTaskParams) (*protocol.Task, error) {
	_ = m.upsertTask(params)                  // Get or create task entry. Ignore return.
	m.storeMessage(params.ID, params.Message) // Store the initial user message.

	// Create a cancellable context for this specific task processing
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Ensure context is cancelled eventually

	// Process the task
	err := m.processTaskWithProcessor(taskCtx, params.ID, params.Message)

	// Return the latest task state after processing
	finalTask, e := m.getTaskInternal(params.ID)
	if e != nil {
		log.Errorf("Failed to get task %s after processing: %v", params.ID, e)
	}

	// Do not include e in the return value
	return finalTask, err
}

// OnSendTaskSubscribe handles a tasks/sendSubscribe request with streaming response.
// It creates or updates a task based on the parameters, then returns a channel for status updates.
// The channel will receive events until the task completes, fails, is cancelled, or the context expires.
func (m *MemoryTaskManager) OnSendTaskSubscribe(
	ctx context.Context,
	params protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	// Create a new task or update an existing one
	task := m.upsertTask(params)
	// Store the message that came with the request
	m.storeMessage(params.ID, params.Message)

	// Create event channel for this specific subscriber
	eventChan := make(chan protocol.TaskEvent, 10) // Buffered to prevent blocking sends
	m.addSubscriber(params.ID, eventChan)

	// Create a cancellable context for the processor
	processorCtx, cancel := context.WithCancel(ctx)

	// Store the cancel function
	m.ContextsMutex.Lock()
	m.Contexts[params.ID] = cancel
	m.ContextsMutex.Unlock()

	// Set initial state if new (submitted -> working)
	// This will generate the first event for subscribers
	if task.Status.State == protocol.TaskStateSubmitted {
		if err := m.UpdateTaskStatus(params.ID, protocol.TaskStateWorking, nil); err != nil {
			m.removeSubscriber(params.ID, eventChan)
			close(eventChan)
			return nil, err
		}
	}

	// Start the processor in a goroutine
	m.startTaskSubscribe(processorCtx, params.ID, params.Message)

	// Return the channel for events
	return eventChan, nil
}

// getTaskInternal retrieves the task without locking (caller must handle locks).
// Returns nil if not found.
func (m *MemoryTaskManager) getTaskInternal(taskID string) (*protocol.Task, error) {
	return m.getTaskWithValidation(taskID)
}

// OnGetTask retrieves the current state of a task, including optional message history.
// It implements the TaskManager interface.
func (m *MemoryTaskManager) OnGetTask(ctx context.Context, params protocol.TaskQueryParams) (*protocol.Task, error) {
	task, err := m.getTaskWithValidation(params.ID)
	if err != nil {
		return nil, err // Already an ErrTaskNotFound or similar.
	}
	// Add message history if requested.
	if params.HistoryLength != nil {
		// historyLength == 0 means "get all history"
		// historyLength > 0 means "get that many most recent messages"
		// historyLength == nil means "don't include history"
		m.MessagesMutex.RLock()
		messages, historyExists := m.Messages[params.ID]
		m.MessagesMutex.RUnlock()
		if historyExists {
			historyLen := len(messages)
			requestedLen := *params.HistoryLength
			var startIndex int
			if requestedLen > 0 && requestedLen < historyLen {
				startIndex = historyLen - requestedLen
			}
			// Make a copy of the history slice.
			task.History = make([]protocol.Message, len(messages[startIndex:]))
			copy(task.History, messages[startIndex:])
			// --> Add logging here to check type immediately after copy.
			if len(task.History) > 0 && len(task.History[0].Parts) > 0 {
				log.Debugf("DEBUG: Type in task.History[0].Parts[0] inside OnGetTask: %T", task.History[0].Parts[0])
			}
			return task, nil
		}
	}
	task.History = nil // Ensure history is nil if not requested.
	return task, nil
}

// OnCancelTask attempts to cancel an ongoing task.
// It implements the TaskManager interface.
func (m *MemoryTaskManager) OnCancelTask(ctx context.Context, params protocol.TaskIDParams) (*protocol.Task, error) {
	m.TasksMutex.Lock()
	task, exists := m.Tasks[params.ID]
	if !exists {
		m.TasksMutex.Unlock()
		return nil, ErrTaskNotFound(params.ID)
	}
	m.TasksMutex.Unlock() // Release lock before potential context cancel / status update.
	// Check if task is already in a final state (read lock again).
	m.TasksMutex.RLock()
	alreadyFinal := isFinalState(task.Status.State)
	m.TasksMutex.RUnlock()
	if alreadyFinal {
		return task, ErrTaskFinalState(params.ID, task.Status.State)
	}
	// Find and call the context cancel func stored for this taskID.
	var cancelFound bool
	m.ContextsMutex.Lock()
	cancel, exists := m.Contexts[params.ID]
	if exists {
		cancel() // Call the cancel function.
		cancelFound = true
		// Don't delete the context here - let the processor goroutine clean up.
	}
	m.ContextsMutex.Unlock()
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
	updatedTask, err := m.getTaskInternal(params.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task %s after cancellation update: %w", params.ID, err)
	}
	return updatedTask, nil
}

// UpdateTaskStatus updates the task's state and notifies any subscribers.
// Returns an error if the task does not exist.
// Exported method (used by memoryTaskHandle).
func (m *MemoryTaskManager) UpdateTaskStatus(taskID string, state protocol.TaskState, message *protocol.Message) error {
	m.TasksMutex.Lock()
	task, exists := m.Tasks[taskID]
	if !exists {
		m.TasksMutex.Unlock()
		log.Warnf("Warning: UpdateTaskStatus called for non-existent task %s", taskID)
		return ErrTaskNotFound(taskID)
	}
	// Update status fields.
	task.Status = protocol.TaskStatus{
		State:     state,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	// Create a copy for notification before unlocking.
	taskCopy := *task
	m.TasksMutex.Unlock() // Unlock before potentially blocking on channel send.
	// Store the message in history if provided
	if message != nil {
		// Convert TaskStatus Message (which is a pointer) to a Message value for history
		m.storeMessage(taskID, *message)
	}
	// Notify subscribers outside the lock.
	m.notifySubscribers(taskID, protocol.TaskStatusUpdateEvent{
		ID:     taskID,
		Status: taskCopy.Status,
		Final:  isFinalState(state),
	})
	return nil
}

// AddArtifact adds an artifact to the task and notifies subscribers.
// Returns an error if the task does not exist.
// Exported method (used by memoryTaskHandle).
func (m *MemoryTaskManager) AddArtifact(taskID string, artifact protocol.Artifact) error {
	m.TasksMutex.Lock()
	task, exists := m.Tasks[taskID]
	if !exists {
		m.TasksMutex.Unlock()
		log.Warnf("Warning: AddArtifact called for non-existent task %s", taskID)
		return ErrTaskNotFound(taskID)
	}
	// Append the artifact.
	if task.Artifacts == nil {
		task.Artifacts = make([]protocol.Artifact, 0, 1)
	}
	task.Artifacts = append(task.Artifacts, artifact)
	// Create copies for notification before unlocking.
	m.TasksMutex.Unlock() // Unlock before potentially blocking on channel send.
	// Notify subscribers outside the lock.
	finalEvent := artifact.LastChunk != nil && *artifact.LastChunk
	m.notifySubscribers(taskID, protocol.TaskArtifactUpdateEvent{
		ID:       taskID,
		Artifact: artifact,
		Final:    finalEvent,
	})
	return nil
}

// --- Internal Helper Methods (Unexported) ---

// upsertTask creates a new task or updates metadata if it already exists.
// Assumes locks are handled by the caller if needed, but acquires its own lock.
func (m *MemoryTaskManager) upsertTask(params protocol.SendTaskParams) *protocol.Task {
	m.TasksMutex.Lock()
	defer m.TasksMutex.Unlock()
	task, exists := m.Tasks[params.ID]
	if !exists {
		task = protocol.NewTask(params.ID, params.SessionID)
		m.Tasks[params.ID] = task
		log.Infof("Created new task %s (Session: %v)", params.ID, params.SessionID)
	} else {
		log.Debugf("Updating existing task %s", params.ID)
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
	return task
}

// storeMessage adds a message to the task's history.
// Assumes locks are handled by the caller if needed, but acquires its own lock.
func (m *MemoryTaskManager) storeMessage(taskID string, message protocol.Message) {
	m.MessagesMutex.Lock()
	defer m.MessagesMutex.Unlock()
	if _, exists := m.Messages[taskID]; !exists {
		m.Messages[taskID] = make([]protocol.Message, 0, 1) // Initialize with capacity.
	}
	// Create a copy of the message to store, ensuring history isolation.
	messageCopy := protocol.Message{
		Role:     message.Role,
		Metadata: message.Metadata, // Shallow copy of map is usually fine.
	}
	if message.Parts != nil {
		// Copy the slice of parts (shallow copy of interface values is correct).
		messageCopy.Parts = make([]protocol.Part, len(message.Parts))
		copy(messageCopy.Parts, message.Parts)
	}
	m.Messages[taskID] = append(m.Messages[taskID], messageCopy)
}

// addSubscriber adds a channel to the list of subscribers for a task.
func (m *MemoryTaskManager) addSubscriber(taskID string, ch chan<- protocol.TaskEvent) {
	m.SubMutex.Lock()
	defer m.SubMutex.Unlock()
	if _, exists := m.Subscribers[taskID]; !exists {
		m.Subscribers[taskID] = make([]chan<- protocol.TaskEvent, 0, 1)
	}
	m.Subscribers[taskID] = append(m.Subscribers[taskID], ch)
	log.Debugf("Added subscriber for task %s", taskID)
}

// removeSubscriber removes a specific channel from the list of subscribers for a task.
func (m *MemoryTaskManager) removeSubscriber(taskID string, ch chan<- protocol.TaskEvent) {
	m.SubMutex.Lock()
	defer m.SubMutex.Unlock()
	channels, exists := m.Subscribers[taskID]
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
	if len(newChannels) == 0 {
		delete(m.Subscribers, taskID) // No more subscribers.
	} else {
		m.Subscribers[taskID] = newChannels
	}
	log.Debugf("Removed subscriber for task %s", taskID)
}

// notifySubscribers sends an event to all current subscribers of a task.
func (m *MemoryTaskManager) notifySubscribers(taskID string, event protocol.TaskEvent) {
	m.SubMutex.RLock()
	subs, exists := m.Subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.SubMutex.RUnlock()
		return // No subscribers to notify.
	}
	// Copy the slice of channels under read lock.
	subsCopy := make([]chan<- protocol.TaskEvent, len(subs))
	copy(subsCopy, subs)
	m.SubMutex.RUnlock()
	log.Debugf("Notifying %d subscribers for task %s (Event Type: %T, Final: %t)",
		len(subsCopy), taskID, event, event.IsFinal())
	// Send events outside the lock.
	for _, ch := range subsCopy {
		// Use a select with a default case for a non-blocking send.
		// This prevents one slow/blocked subscriber from delaying others.
		select {
		case ch <- event:
			// Event sent successfully.
		default:
			// Channel buffer is full or channel is closed.
			log.Warnf("Warning: Dropping event for task %s subscriber - channel full or closed.", taskID)
		}
	}
}

// OnPushNotificationSet implements TaskManager.OnPushNotificationSet.
// It sets push notification configuration for a task.
func (m *MemoryTaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	m.TasksMutex.RLock()
	_, exists := m.Tasks[params.ID]
	m.TasksMutex.RUnlock()
	if !exists {
		return nil, ErrTaskNotFound(params.ID)
	}
	// Store the push notification configuration.
	m.PushNotificationsMutex.Lock()
	m.PushNotifications[params.ID] = params.PushNotificationConfig
	m.PushNotificationsMutex.Unlock()
	log.Infof("Set push notification for task %s to URL: %s", params.ID, params.PushNotificationConfig.URL)
	// Return the stored configuration as confirmation.
	return &params, nil
}

// OnPushNotificationGet implements TaskManager.OnPushNotificationGet.
// It retrieves the push notification configuration for a task.
func (m *MemoryTaskManager) OnPushNotificationGet(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	m.TasksMutex.RLock()
	_, exists := m.Tasks[params.ID]
	m.TasksMutex.RUnlock()
	if !exists {
		return nil, ErrTaskNotFound(params.ID)
	}
	// Retrieve the push notification configuration.
	m.PushNotificationsMutex.RLock()
	config, exists := m.PushNotifications[params.ID]
	m.PushNotificationsMutex.RUnlock()
	if !exists {
		// Task exists but has no push notification config.
		return nil, ErrPushNotificationNotConfigured(params.ID)
	}
	result := &protocol.TaskPushNotificationConfig{
		ID:                     params.ID,
		PushNotificationConfig: config,
	}
	return result, nil
}

// OnResubscribe implements TaskManager.OnResubscribe.
// It allows a client to reestablish an SSE stream for an existing task.
func (m *MemoryTaskManager) OnResubscribe(ctx context.Context, params protocol.TaskIDParams) (<-chan protocol.TaskEvent, error) {
	m.TasksMutex.RLock()
	task, exists := m.Tasks[params.ID]
	m.TasksMutex.RUnlock()
	if !exists {
		return nil, ErrTaskNotFound(params.ID)
	}
	// Create a channel for events.
	eventChan := make(chan protocol.TaskEvent)
	// For tasks in final state, just send a status update event and close.
	if isFinalState(task.Status.State) {
		go func() {
			// Send a task status update event.
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

// processError checks the error type and returns the appropriate task manager error.
// If the error already has the right format, it returns it directly.
func processError(err error) error {
	if err == nil {
		return nil
	}
	// Check if it's a task not found error message
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return ErrTaskNotFound(err.Error())
	}
	// For other errors, return a generic internal error
	return err
}

// getTaskWithValidation gets a task and validates it exists.
// Returns task and nil if found, nil and error if not found.
func (m *MemoryTaskManager) getTaskWithValidation(taskID string) (*protocol.Task, error) {
	m.TasksMutex.RLock()
	task, exists := m.Tasks[taskID]
	m.TasksMutex.RUnlock()

	if !exists {
		return nil, ErrTaskNotFound(taskID)
	}
	taskCopy := *task // Return a copy.
	return &taskCopy, nil
}
