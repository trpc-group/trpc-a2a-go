// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// MockMessageProcessor implements MessageProcessor for testing
type MockMessageProcessor struct {
	ProcessMessageFunc func(ctx context.Context, message protocol.Message, options ProcessOptions, handle TaskHandler) (*MessageProcessingResult, error)
}

func (m *MockMessageProcessor) ProcessMessage(ctx context.Context, message protocol.Message, options ProcessOptions, handle TaskHandler) (*MessageProcessingResult, error) {
	if m.ProcessMessageFunc != nil {
		return m.ProcessMessageFunc(ctx, message, options, handle)
	}

	// Default implementation: echo the message
	response := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []*protocol.Part{
			protocol.NewTextPart("Echo: " + getTextFromMessage(message)),
		},
	}

	return &MessageProcessingResult{
		Result: &protocol.SendMessageResponse{Message: response},
	}, nil
}

// Helper function to extract text from message
func getTextFromMessage(message protocol.Message) string {
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

func TestNewMemoryTaskManager(t *testing.T) {
	tests := []struct {
		name      string
		processor MessageProcessor
		options   []MemoryTaskManagerOption
		wantErr   bool
	}{
		{
			name:      "valid processor",
			processor: &MockMessageProcessor{},
			wantErr:   false,
		},
		{
			name:      "nil processor",
			processor: nil,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewMemoryTaskManager(tt.processor, tt.options...)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if manager == nil {
				t.Error("Expected manager but got nil")
				return
			}

			if manager.Processor != tt.processor {
				t.Error("Processor not set correctly")
			}

			if len(tt.options) > 0 && manager.options.MaxHistoryLength != 50 {
				t.Errorf("Expected MaxHistoryLength=50, got %d", manager.options.MaxHistoryLength)
			}
		})
	}
}

func TestMemoryTaskManager_OnSendMessage(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name    string
		request protocol.SendMessageParams
		wantErr bool
	}{
		{
			name: "valid message",
			request: protocol.SendMessageParams{
				Message: protocol.Message{
					Role: protocol.MessageRoleUser,
					Parts: []*protocol.Part{
						protocol.NewTextPart("Hello"),
					},
				},
			},
			wantErr: false,
		},
		{
			name: "message with context",
			request: protocol.SendMessageParams{
				Message: protocol.Message{
					Role:      protocol.MessageRoleUser,
					ContextID: stringPtr("test-context"),
					Parts: []*protocol.Part{
						protocol.NewTextPart("Hello with context"),
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := manager.OnSendMessage(ctx, tt.request)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("Expected result but got nil")
				return
			}

			// Check if result contains a message
			if result.Message != nil {
				if result.Message.MessageID == "" {
					t.Error("Expected message ID to be set")
				}

				// Check that message is in storage
				manager.mu.RLock()
				_, exists := manager.Messages[result.Message.MessageID]
				manager.mu.RUnlock()

				if !exists {
					t.Error("Message not found in storage")
				}
			}
		})
	}
}

func TestMemoryTaskManager_OnSendMessageStream(t *testing.T) {
	processor := &MockMessageProcessor{
		ProcessMessageFunc: func(ctx context.Context, message protocol.Message, options ProcessOptions, handle TaskHandler) (*MessageProcessingResult, error) {
			// Create a task for streaming
			taskID, err := handle.BuildTask(nil, message.ContextID)
			if err != nil {
				return nil, err
			}

			subscriber, err := handle.SubscribeTask(&taskID)
			if err != nil {
				return nil, err
			}

			// Simulate async processing
			go func() {
				defer subscriber.Close()

				// Send initial status update
				handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, nil)

				// Complete task
				finalMessage := &protocol.Message{
					Role: protocol.MessageRoleAgent,
					Parts: []*protocol.Part{
						protocol.NewTextPart("Streaming completed"),
					},
				}
				handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, finalMessage)
			}()

			return &MessageProcessingResult{
				StreamingEvents: subscriber,
			}, nil
		},
	}

	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	request := protocol.SendMessageParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []*protocol.Part{
				protocol.NewTextPart("Stream test"),
			},
		},
	}

	eventChan, err := manager.OnSendMessageStream(ctx, request)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if eventChan == nil {
		t.Fatal("Expected event channel but got nil")
	}

	// Collect events with shorter timeout
	var events []protocol.StreamResponse
	timeout := time.After(500 * time.Millisecond)
	eventCount := 0

	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				// Channel closed, test completed
				goto CheckEvents
			}
			events = append(events, event)
			eventCount++

			// Stop after receiving some events to avoid infinite loop
			if eventCount >= 10 {
				goto CheckEvents
			}

		case <-timeout:
			// Don't fail on timeout, just check what we got
			goto CheckEvents
		}
	}

CheckEvents:
	if len(events) == 0 {
		t.Error("Expected at least one event")
		return
	}

	t.Logf("Received %d events", len(events))

	// Should have received some events
	hasStatusUpdate := false
	for _, event := range events {
		if event.StatusUpdate != nil {
			hasStatusUpdate = true
			break
		}
	}

	if !hasStatusUpdate {
		t.Error("Expected at least one status update event")
	}
}

func TestMemoryTaskManager_OnGetTask(t *testing.T) {
	processor := &MockMessageProcessor{
		ProcessMessageFunc: func(ctx context.Context, message protocol.Message, options ProcessOptions, handle TaskHandler) (*MessageProcessingResult, error) {
			// Create a task for testing
			taskID, err := handle.BuildTask(nil, message.ContextID)
			if err != nil {
				return nil, err
			}

			// Get the actual task object
			task, err := handle.GetTask(&taskID)
			if err != nil {
				return nil, err
			}

			return &MessageProcessingResult{
				Result: &protocol.SendMessageResponse{Task: task.Task()},
			}, nil
		},
	}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()

	// First create a task by sending a message
	request := protocol.SendMessageParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []*protocol.Part{
				protocol.NewTextPart("Test"),
			},
		},
	}

	result, err := manager.OnSendMessage(ctx, request)
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	var existingTaskID string
	if result.Task != nil {
		existingTaskID = result.Task.ID
	} else {
		t.Fatal("Expected task result but got nil")
	}

	tests := []struct {
		name     string
		params   protocol.TaskQueryParams
		wantErr  bool
		validate func(*testing.T, *protocol.Task, error)
	}{
		{
			name: "get existing task",
			params: protocol.TaskQueryParams{
				ID: existingTaskID,
			},
			wantErr: false,
			validate: func(t *testing.T, task *protocol.Task, err error) {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if task == nil {
					t.Error("Expected task but got nil")
				}
				if task != nil && task.ID != existingTaskID {
					t.Errorf("Expected task ID %s, got %s", existingTaskID, task.ID)
				}
			},
		},
		{
			name: "get non-existent task",
			params: protocol.TaskQueryParams{
				ID: "non-existent-task",
			},
			wantErr: true,
			validate: func(t *testing.T, task *protocol.Task, err error) {
				if err == nil {
					t.Error("Expected error for non-existent task")
				}
				if task != nil {
					t.Error("Expected nil task for error case")
				}
			},
		},
		{
			name: "empty task ID",
			params: protocol.TaskQueryParams{
				ID: "",
			},
			wantErr: true,
			validate: func(t *testing.T, task *protocol.Task, err error) {
				if err == nil {
					t.Error("Expected error for empty task ID")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getTask, err := manager.OnGetTask(ctx, tt.params)
			tt.validate(t, getTask, err)
		})
	}
}

func TestMemoryTaskManager_OnCancelTask(t *testing.T) {
	processor := &MockMessageProcessor{
		ProcessMessageFunc: func(ctx context.Context, message protocol.Message, options ProcessOptions, handle TaskHandler) (*MessageProcessingResult, error) {
			// Create a task for testing cancellation
			taskID, err := handle.BuildTask(nil, message.ContextID)
			if err != nil {
				return nil, err
			}

			// Get the actual task object
			task, err := handle.GetTask(&taskID)
			if err != nil {
				return nil, err
			}

			return &MessageProcessingResult{
				Result: &protocol.SendMessageResponse{Task: task.Task()},
			}, nil
		},
	}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Create a task first
	request := protocol.SendMessageParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []*protocol.Part{
				protocol.NewTextPart("Test"),
			},
		},
	}

	result, err := manager.OnSendMessage(ctx, request)
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Extract task from result
	var taskID string
	if result.Task != nil {
		taskID = result.Task.ID
	} else {
		t.Fatal("Expected task result but got nil")
	}

	// Cancel the task
	cancelParams := protocol.TaskIDParams{
		ID: taskID,
	}

	canceledTask, err := manager.OnCancelTask(ctx, cancelParams)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if canceledTask == nil {
		t.Error("Expected canceled task but got nil")
	}

	if canceledTask.Status.State != protocol.TaskStateCanceled {
		t.Errorf("Expected task state to be canceled, got %s", canceledTask.Status.State)
	}
}

func TestMemoryTaskManager_PushNotifications(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name      string
		action    string // "set" or "get"
		taskID    string
		config    *protocol.TaskPushNotificationConfig
		getParams *protocol.TaskIDParams
		wantErr   bool
		validate  func(*testing.T, interface{}, error)
	}{
		{
			name:   "set push notification",
			action: "set",
			taskID: "test-task-id",
			config: &protocol.TaskPushNotificationConfig{
				TaskID: "test-task-id",
				URL:    "https://example.com/webhook",
				Token:  "Bearer token",
			},
			wantErr: false,
			validate: func(t *testing.T, result interface{}, err error) {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result == nil {
					t.Error("Expected set result but got nil")
				}
			},
		},
		{
			name:   "get push notification",
			action: "get",
			taskID: "test-task-id",
			getParams: &protocol.TaskIDParams{
				ID: "test-task-id",
			},
			wantErr: false,
			validate: func(t *testing.T, result interface{}, err error) {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result == nil {
					t.Error("Expected get result but got nil")
					return
				}

				if getResult, ok := result.(*protocol.TaskPushNotificationConfig); ok {
					expectedURL := "https://example.com/webhook"
					if getResult.URL != expectedURL {
						t.Errorf("Expected URL %s, got %s", expectedURL, getResult.URL)
					}
				} else {
					t.Errorf("Expected TaskPushNotificationConfig, got %T", result)
				}
			},
		},
		{
			name:   "get non-existent push notification",
			action: "get",
			taskID: "non-existent-task",
			getParams: &protocol.TaskIDParams{
				ID: "non-existent-task",
			},
			wantErr: true,
			validate: func(t *testing.T, result interface{}, err error) {
				if err == nil {
					t.Error("Expected error for non-existent task")
				}
			},
		},
	}

	// First set up a push notification for the get test
	setupConfig := protocol.TaskPushNotificationConfig{
		TaskID: "test-task-id",
		URL:    "https://example.com/webhook",
		Token:  "Bearer token",
	}
	_, err = manager.OnPushNotificationSet(ctx, setupConfig)
	if err != nil {
		t.Fatalf("Failed to set up push notification: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result interface{}
			var err error

			switch tt.action {
			case "set":
				if tt.config != nil {
					result, err = manager.OnPushNotificationSet(ctx, *tt.config)
				}
			case "get":
				if tt.getParams != nil {
					result, err = manager.OnPushNotificationGet(ctx, *tt.getParams)
				}
			default:
				t.Fatalf("Unknown action: %s", tt.action)
			}

			tt.validate(t, result, err)
		})
	}
}

func TestTaskSubscriber(t *testing.T) {
	tests := []struct {
		name     string
		taskID   string
		capacity int
		setup    func(*MemoryTaskSubscriber)             // Setup function to perform actions
		validate func(*testing.T, *MemoryTaskSubscriber) // Validation function
	}{
		{
			name:     "create subscriber",
			taskID:   "test-task",
			capacity: 5,
			setup:    func(s *MemoryTaskSubscriber) {},
			validate: func(t *testing.T, s *MemoryTaskSubscriber) {
				if s.taskID != "test-task" {
					t.Errorf("Expected task ID %s, got %s", "test-task", s.taskID)
				}
				if s.Closed() {
					t.Error("Expected subscriber to be open")
				}
			},
		},
		{
			name:     "send and receive event",
			taskID:   "test-task-2",
			capacity: 5,
			setup: func(s *MemoryTaskSubscriber) {
				event := protocol.NewStreamResponseMessage(&protocol.Message{
					Role: protocol.MessageRoleAgent,
					Parts: []*protocol.Part{
						protocol.NewTextPart("Test event"),
					},
				})
				err := s.Send(event)
				if err != nil {
					t.Errorf("Unexpected error sending event: %v", err)
				}
			},
			validate: func(t *testing.T, s *MemoryTaskSubscriber) {
				select {
				case receivedEvent := <-s.eventQueue:
					if receivedEvent.Message == nil {
						t.Error("Expected event message but got nil")
					}
				case <-time.After(100 * time.Millisecond):
					t.Error("Timeout waiting for event")
				}
			},
		},
		{
			name:     "close subscriber",
			taskID:   "test-task-3",
			capacity: 5,
			setup: func(s *MemoryTaskSubscriber) {
				s.Close()
			},
			validate: func(t *testing.T, s *MemoryTaskSubscriber) {
				if !s.Closed() {
					t.Error("Expected subscriber to be closed")
				}

				// Test sending to closed subscriber
				event := protocol.NewStreamResponseMessage(&protocol.Message{Role: protocol.MessageRoleAgent})
				err := s.Send(event)
				if err == nil {
					t.Error("Expected error when sending to closed subscriber")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subscriber := NewMemoryTaskSubscriber(tt.taskID, tt.capacity)

			tt.setup(subscriber)
			tt.validate(t, subscriber)
		})
	}
}

func TestCancellableTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	task := &MemoryCancellableTask{
		task: protocol.Task{
			ID:     "test-task",
			Status: protocol.TaskStatus{State: protocol.TaskStateSubmitted},
		},
		cancelFunc: cancel,
		ctx:        ctx,
	}

	// Test cancellation
	task.Cancel()

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected context to be canceled")
	}
}

func TestMemoryTaskManager_UpdateTaskState_CleansSubscribersOnFinalState(t *testing.T) {
	handler, manager := setupTestHandler(t)

	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	sub, err := handler.SubscribeTask(&taskID)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Verify subscriber exists
	manager.taskMu.RLock()
	if len(manager.Subscribers[taskID]) != 1 {
		t.Fatalf("Expected 1 subscriber, got %d", len(manager.Subscribers[taskID]))
	}
	manager.taskMu.RUnlock()

	// Drain events before completing
	go func() {
		for range sub.Channel() {
		}
	}()

	// Transition to final state
	err = handler.UpdateTaskState(&taskID, protocol.TaskStateCompleted, nil)
	if err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	// Subscribers should be cleaned up
	manager.taskMu.RLock()
	subs := manager.Subscribers[taskID]
	manager.taskMu.RUnlock()

	if len(subs) != 0 {
		t.Errorf("Expected subscribers to be cleaned after final state, got %d", len(subs))
	}
}

func TestMemoryTaskManager_cleanExpiredTasks(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Create a task and move it to a final state with an old timestamp
	task := protocol.Task{
		ID: "expired-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCompleted,
			Timestamp: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	cancellableTask := NewCancellableTask(task)

	manager.taskMu.Lock()
	manager.Tasks["expired-task"] = cancellableTask
	manager.Subscribers["expired-task"] = []*MemoryTaskSubscriber{
		NewMemoryTaskSubscriber("expired-task", 10),
	}
	manager.taskMu.Unlock()

	// Create a non-expired task
	activeTask := protocol.Task{
		ID: "active-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateWorking,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	activeCancellable := NewCancellableTask(activeTask)

	manager.taskMu.Lock()
	manager.Tasks["active-task"] = activeCancellable
	manager.taskMu.Unlock()

	// Create a recently completed task (should NOT be cleaned)
	recentTask := protocol.Task{
		ID: "recent-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCompleted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	recentCancellable := NewCancellableTask(recentTask)

	manager.taskMu.Lock()
	manager.Tasks["recent-task"] = recentCancellable
	manager.taskMu.Unlock()

	// TTL=0 should skip cleanup entirely
	skipped := manager.cleanExpiredTasks(0)
	if skipped != 0 {
		t.Errorf("Expected 0 cleaned tasks with TTL=0, got %d", skipped)
	}
	manager.taskMu.RLock()
	if _, exists := manager.Tasks["expired-task"]; !exists {
		t.Error("Expired task should still exist when TTL=0")
	}
	manager.taskMu.RUnlock()

	// Clean with 1 hour TTL
	cleaned := manager.cleanExpiredTasks(1 * time.Hour)

	if cleaned != 1 {
		t.Errorf("Expected 1 cleaned task, got %d", cleaned)
	}

	manager.taskMu.RLock()
	defer manager.taskMu.RUnlock()

	if _, exists := manager.Tasks["expired-task"]; exists {
		t.Error("Expected expired task to be removed")
	}
	if _, exists := manager.Subscribers["expired-task"]; exists {
		t.Error("Expected expired task subscribers to be removed")
	}
	if _, exists := manager.Tasks["active-task"]; !exists {
		t.Error("Active task should not be removed")
	}
	if _, exists := manager.Tasks["recent-task"]; !exists {
		t.Error("Recently completed task should not be removed")
	}
}

func TestMemoryTaskManager_Close(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Add some tasks and subscribers
	task := protocol.Task{
		ID: "close-test-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateWorking,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	cancellableTask := NewCancellableTask(task)
	sub := NewMemoryTaskSubscriber("close-test-task", 10)

	manager.taskMu.Lock()
	manager.Tasks["close-test-task"] = cancellableTask
	manager.Subscribers["close-test-task"] = []*MemoryTaskSubscriber{sub}
	manager.taskMu.Unlock()

	manager.Close()

	if !sub.Closed() {
		t.Error("Expected subscriber to be closed after manager.Close()")
	}

	manager.taskMu.RLock()
	defer manager.taskMu.RUnlock()

	if len(manager.Tasks) != 0 {
		t.Errorf("Expected all tasks to be cleaned, got %d", len(manager.Tasks))
	}
	if len(manager.Subscribers) != 0 {
		t.Errorf("Expected all subscribers to be cleaned, got %d", len(manager.Subscribers))
	}
}

func TestMemoryTaskManager_Close_Idempotent(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Calling Close multiple times must not panic.
	manager.Close()
	manager.Close()
}

func TestWithTaskTTL(t *testing.T) {
	opts := DefaultMemoryTaskManagerOptions()

	if opts.TaskTTL != 0 {
		t.Errorf("Expected default TaskTTL=0 (disabled), got %v", opts.TaskTTL)
	}

	WithTaskTTL(30 * time.Minute)(opts)
	if opts.TaskTTL != 30*time.Minute {
		t.Errorf("Expected TaskTTL=30m, got %v", opts.TaskTTL)
	}

	// Zero value explicitly disables task cleanup
	WithTaskTTL(0)(opts)
	if opts.TaskTTL != 0 {
		t.Errorf("Expected TaskTTL=0 after explicit disable, got %v", opts.TaskTTL)
	}
}

func stringPtr(s string) *string {
	return &s
}
