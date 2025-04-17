// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.
package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// getCurrentTimestamp returns the current time in ISO 8601 format
func getCurrentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// mockTaskManager implements the taskmanager.TaskManager interface for testing.
type mockTaskManager struct {
	mu sync.Mutex
	// Store tasks for basic Get/Cancel simulation
	tasks map[string]*protocol.Task

	// Configure responses/behavior for testing
	SendResponse    *protocol.Task
	SendError       error
	GetResponse     *protocol.Task
	GetError        error
	CancelResponse  *protocol.Task
	CancelError     error
	SubscribeEvents []protocol.TaskEvent // Events to send for subscription
	SubscribeError  error

	// Push notification fields
	pushNotificationSetResponse *protocol.TaskPushNotificationConfig
	pushNotificationSetError    error
	pushNotificationGetResponse *protocol.TaskPushNotificationConfig
	pushNotificationGetError    error
}

// newMockTaskManager creates a new mockTaskManager for testing.
func newMockTaskManager() *mockTaskManager {
	return &mockTaskManager{
		tasks: make(map[string]*protocol.Task),
	}
}

// OnSendTask implements the TaskManager interface.
func (m *mockTaskManager) OnSendTask(
	ctx context.Context,
	params protocol.SendTaskParams,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return configured error if set
	if m.SendError != nil {
		return nil, m.SendError
	}

	// Validate required fields
	if params.ID == "" {
		return nil, jsonrpc.ErrInvalidParams("task ID is required")
	}

	if len(params.Message.Parts) == 0 {
		return nil, jsonrpc.ErrInvalidParams("message must have at least one part")
	}

	// Return configured response if set
	if m.SendResponse != nil {
		// Store for later retrieval
		m.tasks[m.SendResponse.ID] = m.SendResponse
		return m.SendResponse, nil
	}

	// Default behavior: create a simple task
	task := protocol.NewTask(params.ID, params.SessionID)
	now := getCurrentTimestamp()
	task.Status = protocol.TaskStatus{
		State:     protocol.TaskStateSubmitted,
		Timestamp: now,
	}

	// Store for later retrieval
	m.tasks[task.ID] = task
	return task, nil
}

// OnGetTask implements the TaskManager interface.
func (m *mockTaskManager) OnGetTask(
	ctx context.Context, params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.GetError != nil {
		return nil, m.GetError
	}

	if m.GetResponse != nil {
		return m.GetResponse, nil
	}

	// Check if task exists
	task, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}
	return task, nil
}

// OnCancelTask implements the TaskManager interface.
func (m *mockTaskManager) OnCancelTask(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.CancelError != nil {
		return nil, m.CancelError
	}

	if m.CancelResponse != nil {
		return m.CancelResponse, nil
	}

	// Check if task exists
	task, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// Update task status to canceled
	task.Status.State = protocol.TaskStateCanceled
	task.Status.Timestamp = getCurrentTimestamp()
	return task, nil
}

// OnSendTaskSubscribe implements the TaskManager interface.
func (m *mockTaskManager) OnSendTaskSubscribe(
	ctx context.Context, params protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.SubscribeError != nil {
		return nil, m.SubscribeError
	}

	// Create a task like OnSendTask would
	task := protocol.NewTask(params.ID, params.SessionID)
	task.Status = protocol.TaskStatus{
		State:     protocol.TaskStateSubmitted,
		Timestamp: getCurrentTimestamp(),
	}

	// Store for later retrieval
	m.tasks[task.ID] = task

	// Create a channel and send events
	eventCh := make(chan protocol.TaskEvent, len(m.SubscribeEvents)+1)

	// Send configured events in background
	if len(m.SubscribeEvents) > 0 {
		go func() {
			for _, event := range m.SubscribeEvents {
				select {
				case <-ctx.Done():
					close(eventCh)
					return
				case eventCh <- event:
					// If this is the final event, close the channel
					if event.IsFinal() {
						close(eventCh)
						return
					}
				}
			}
			// If we didn't have a final event, close the channel anyway
			close(eventCh)
		}()
	} else {
		// No events configured, send a default working and completed status
		go func() {
			// Working status
			workingEvent := protocol.TaskStatusUpdateEvent{
				ID: params.ID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateWorking,
					Timestamp: getCurrentTimestamp(),
				},
				Final: false,
			}

			// Completed status
			completedEvent := protocol.TaskStatusUpdateEvent{
				ID: params.ID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateCompleted,
					Timestamp: getCurrentTimestamp(),
				},
				Final: true,
			}

			select {
			case <-ctx.Done():
				close(eventCh)
				return
			case eventCh <- workingEvent:
				// Continue
			}

			select {
			case <-ctx.Done():
				close(eventCh)
				return
			case eventCh <- completedEvent:
				close(eventCh)
				return
			}
		}()
	}

	return eventCh, nil
}

// OnPushNotificationSet implements the TaskManager interface for push notifications.
func (m *mockTaskManager) OnPushNotificationSet(
	ctx context.Context, params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pushNotificationSetError != nil {
		return nil, m.pushNotificationSetError
	}

	if m.pushNotificationSetResponse != nil {
		return m.pushNotificationSetResponse, nil
	}

	// Default implementation if response not configured
	return &protocol.TaskPushNotificationConfig{
		ID:                     params.ID,
		PushNotificationConfig: params.PushNotificationConfig,
	}, nil
}

// OnPushNotificationGet implements the TaskManager interface for push notifications.
func (m *mockTaskManager) OnPushNotificationGet(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pushNotificationGetError != nil {
		return nil, m.pushNotificationGetError
	}

	if m.pushNotificationGetResponse != nil {
		return m.pushNotificationGetResponse, nil
	}

	// Default not found response
	return nil, fmt.Errorf("push notification config not found for task %s", params.ID)
}

// OnResubscribe implements the TaskManager interface for resubscribing to task events.
func (m *mockTaskManager) OnResubscribe(
	ctx context.Context, params protocol.TaskIDParams,
) (<-chan protocol.TaskEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.SubscribeError != nil {
		return nil, m.SubscribeError
	}

	// Check if task exists
	_, exists := m.tasks[params.ID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(params.ID)
	}

	// Create a channel and send events
	eventCh := make(chan protocol.TaskEvent, len(m.SubscribeEvents)+1)

	// Send configured events in background
	if len(m.SubscribeEvents) > 0 {
		go func() {
			for _, event := range m.SubscribeEvents {
				select {
				case <-ctx.Done():
					close(eventCh)
					return
				case eventCh <- event:
					// If this is the final event, close the channel
					if event.IsFinal() {
						close(eventCh)
						return
					}
				}
			}
			// If we didn't have a final event, close the channel anyway
			close(eventCh)
		}()
	} else {
		// No events configured, send a default completed status
		go func() {
			completedEvent := protocol.TaskStatusUpdateEvent{
				ID: params.ID,
				Status: protocol.TaskStatus{
					State:     protocol.TaskStateCompleted,
					Timestamp: getCurrentTimestamp(),
				},
				Final: true,
			}

			select {
			case <-ctx.Done():
				close(eventCh)
				return
			case eventCh <- completedEvent:
				close(eventCh)
				return
			}
		}()
	}

	return eventCh, nil
}

// ProcessTask is a helper method for tests that need to process a task directly.
func (m *mockTaskManager) ProcessTask(
	ctx context.Context, taskID string, msg protocol.Message,
) (*protocol.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if task exists
	task, exists := m.tasks[taskID]
	if !exists {
		return nil, taskmanager.ErrTaskNotFound(taskID)
	}

	// Update task status to working
	task.Status.State = protocol.TaskStateWorking
	task.Status.Timestamp = getCurrentTimestamp()

	// Add message to history if it exists
	if task.History == nil {
		task.History = make([]protocol.Message, 0)
	}
	task.History = append(task.History, msg)

	return task, nil
}
