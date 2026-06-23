// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// MockMessageProcessor implements taskmanager.MessageProcessor for testing
type MockMessageProcessor struct {
	ProcessMessageFunc func(ctx context.Context, message protocol.Message, options taskmanager.ProcessOptions, handle taskmanager.TaskHandler) (*taskmanager.MessageProcessingResult, error)
}

func (m *MockMessageProcessor) ProcessMessage(ctx context.Context, message protocol.Message, options taskmanager.ProcessOptions, handle taskmanager.TaskHandler) (*taskmanager.MessageProcessingResult, error) {
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

	return &taskmanager.MessageProcessingResult{
		Result: &protocol.SendMessageResponse{Result: response},
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

func TestNewTaskManager(t *testing.T) {
	tests := []struct {
		name      string
		processor taskmanager.MessageProcessor
		options   []TaskManagerOption
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
			manager, err := NewTaskManager(tt.processor, tt.options...)

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

func TestTaskManager_OnSendMessage(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
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
			if result.GetMessage() != nil {
				if result.GetMessage().MessageID == "" {
					t.Error("Expected message ID to be set")
				}

				// Check that message is in storage
				manager.mu.RLock()
				_, exists := manager.Messages[result.GetMessage().MessageID]
				manager.mu.RUnlock()

				if !exists {
					t.Error("Message not found in storage")
				}
			}
		})
	}
}

func TestTaskManager_OnSendMessageStream(t *testing.T) {
	processor := &MockMessageProcessor{
		ProcessMessageFunc: func(ctx context.Context, message protocol.Message, options taskmanager.ProcessOptions, handle taskmanager.TaskHandler) (*taskmanager.MessageProcessingResult, error) {
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

			return &taskmanager.MessageProcessingResult{
				StreamingEvents: subscriber,
			}, nil
		},
	}

	manager, err := NewTaskManager(processor)
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
		if event.GetStatusUpdate() != nil {
			hasStatusUpdate = true
			break
		}
	}

	if !hasStatusUpdate {
		t.Error("Expected at least one status update event")
	}
}

func TestTaskManager_OnGetTask(t *testing.T) {
	processor := &MockMessageProcessor{
		ProcessMessageFunc: func(ctx context.Context, message protocol.Message, options taskmanager.ProcessOptions, handle taskmanager.TaskHandler) (*taskmanager.MessageProcessingResult, error) {
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

			return &taskmanager.MessageProcessingResult{
				Result: &protocol.SendMessageResponse{Result: task.Task()},
			}, nil
		},
	}
	manager, err := NewTaskManager(processor)
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
	if result.GetTask() != nil {
		existingTaskID = result.GetTask().ID
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

func TestTaskManager_OnCancelTask(t *testing.T) {
	processor := &MockMessageProcessor{
		ProcessMessageFunc: func(ctx context.Context, message protocol.Message, options taskmanager.ProcessOptions, handle taskmanager.TaskHandler) (*taskmanager.MessageProcessingResult, error) {
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

			return &taskmanager.MessageProcessingResult{
				Result: &protocol.SendMessageResponse{Result: task.Task()},
			}, nil
		},
	}
	manager, err := NewTaskManager(processor)
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
	if result.GetTask() != nil {
		taskID = result.GetTask().ID
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

func TestTaskManager_PushNotifications(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
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
		setup    func(*TaskSubscriber)             // Setup function to perform actions
		validate func(*testing.T, *TaskSubscriber) // Validation function
	}{
		{
			name:     "create subscriber",
			taskID:   "test-task",
			capacity: 5,
			setup:    func(s *TaskSubscriber) {},
			validate: func(t *testing.T, s *TaskSubscriber) {
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
			setup: func(s *TaskSubscriber) {
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
			validate: func(t *testing.T, s *TaskSubscriber) {
				select {
				case receivedEvent := <-s.eventQueue:
					if receivedEvent.GetMessage() == nil {
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
			setup: func(s *TaskSubscriber) {
				s.Close()
			},
			validate: func(t *testing.T, s *TaskSubscriber) {
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
			subscriber := NewTaskSubscriber(tt.taskID, tt.capacity)

			tt.setup(subscriber)
			tt.validate(t, subscriber)
		})
	}
}

func TestTaskSubscriber_CloseUnblocksBlockingSend(t *testing.T) {
	subscriber := NewTaskSubscriber(
		"blocking-send-task",
		1,
		WithSubscriberBlockingSend(true),
	)

	if err := subscriber.Send(protocol.StreamResponse{}); err != nil {
		t.Fatalf("Failed to fill subscriber channel: %v", err)
	}

	sendErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		sendErr <- subscriber.Send(protocol.StreamResponse{})
	}()

	<-started
	time.Sleep(10 * time.Millisecond)
	subscriber.Close()

	select {
	case err := <-sendErr:
		if err == nil {
			t.Error("Expected blocked send to return an error after Close")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected Close to unblock blocking send")
	}
}

func TestCancellableTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	task := &CancellableTask{
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

func TestTaskManager_UpdateTaskState_CleansSubscribersOnFinalState(t *testing.T) {
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

	// Collect events until the channel is closed by the final-state cleanup.
	var events []protocol.StreamResponse
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for ev := range sub.Channel() {
			events = append(events, ev)
		}
	}()

	// Transition to final state
	err = handler.UpdateTaskState(&taskID, protocol.TaskStateCompleted, nil)
	if err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	// Reaching a final state must close the subscriber channel (range returns).
	select {
	case <-consumerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber channel was not closed after final state")
	}

	// deliver-then-close ordering must not drop the terminal event: the final
	// TaskStatusUpdateEvent has to arrive before the channel close.
	var gotFinal bool
	for _, ev := range events {
		if ev.GetStatusUpdate() != nil &&
			ev.GetStatusUpdate().Final && ev.GetStatusUpdate().Status.State == protocol.TaskStateCompleted {
			gotFinal = true
		}
	}
	if !gotFinal {
		t.Errorf("Expected a final Completed TaskStatusUpdateEvent before close, got %d events", len(events))
	}

	// Subscribers should be cleaned up
	manager.taskMu.RLock()
	subs := manager.Subscribers[taskID]
	manager.taskMu.RUnlock()

	if len(subs) != 0 {
		t.Errorf("Expected subscribers to be cleaned after final state, got %d", len(subs))
	}
}

func TestTaskManager_cleanupFailedSubscribersClosesRemovedSubscribers(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	taskID := "failed-subscriber-task"
	failedSub := NewTaskSubscriber(taskID, 10)
	activeSub := NewTaskSubscriber(taskID, 10)

	manager.taskMu.Lock()
	manager.Subscribers[taskID] = []*TaskSubscriber{failedSub, activeSub}
	manager.taskMu.Unlock()

	manager.cleanupFailedSubscribers(taskID, []*TaskSubscriber{failedSub})

	if !failedSub.Closed() {
		t.Error("Expected failed subscriber to be closed")
	}
	if activeSub.Closed() {
		t.Error("Expected active subscriber to remain open")
	}

	manager.taskMu.RLock()
	subs := manager.Subscribers[taskID]
	manager.taskMu.RUnlock()

	if len(subs) != 1 || subs[0] != activeSub {
		t.Fatalf("Expected only active subscriber to remain, got %d subscribers", len(subs))
	}
}

func TestTaskManager_cleanExpiredTasks(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
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
	manager.Subscribers["expired-task"] = []*TaskSubscriber{
		NewTaskSubscriber("expired-task", 10),
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

func TestTaskManager_TaskTTLCleanupGoroutine(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(
		processor,
		WithConversationTTL(time.Hour, 5*time.Millisecond),
		WithTaskTTL(time.Nanosecond),
	)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	taskID := "auto-expired-task"
	sub := NewTaskSubscriber(taskID, 10)
	task := protocol.Task{
		ID: taskID,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCompleted,
			Timestamp: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	}

	manager.taskMu.Lock()
	manager.Tasks[taskID] = NewCancellableTask(task)
	manager.Subscribers[taskID] = []*TaskSubscriber{sub}
	manager.taskMu.Unlock()

	manager.mu.Lock()
	manager.PushNotifications[taskID] = protocol.TaskPushNotificationConfig{TaskID: taskID}
	manager.mu.Unlock()

	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		manager.taskMu.RLock()
		_, taskExists := manager.Tasks[taskID]
		_, subsExists := manager.Subscribers[taskID]
		manager.taskMu.RUnlock()

		manager.mu.RLock()
		_, pushExists := manager.PushNotifications[taskID]
		manager.mu.RUnlock()

		if !taskExists && !subsExists && !pushExists && sub.Closed() {
			return
		}

		select {
		case <-deadline:
			t.Fatalf(
				"Timed out waiting for task TTL cleanup: taskExists=%v subsExists=%v pushExists=%v subClosed=%v",
				taskExists,
				subsExists,
				pushExists,
				sub.Closed(),
			)
		case <-ticker.C:
		}
	}
}

func TestTaskManager_Close(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
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
	sub := NewTaskSubscriber("close-test-task", 10)

	manager.taskMu.Lock()
	manager.Tasks["close-test-task"] = cancellableTask
	manager.Subscribers["close-test-task"] = []*TaskSubscriber{sub}
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

func TestTaskManager_Close_Idempotent(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Calling Close multiple times must not panic.
	manager.Close()
	manager.Close()
}

func TestWithTaskTTL(t *testing.T) {
	opts := DefaultTaskManagerOptions()

	if opts.TaskTTL != 0 {
		t.Errorf("Expected default TaskTTL=0 (disabled), got %v", opts.TaskTTL)
	}

	// A positive TTL also enables the cleanup goroutine, mirroring WithConversationTTL.
	fresh := &TaskManagerOptions{}
	WithTaskTTL(30 * time.Minute)(fresh)
	if fresh.TaskTTL != 30*time.Minute {
		t.Errorf("Expected TaskTTL=30m, got %v", fresh.TaskTTL)
	}
	if !fresh.EnableCleanup {
		t.Error("Expected WithTaskTTL(>0) to enable cleanup")
	}

	// A zero TTL disables task cleanup and must not flip EnableCleanup on.
	disabled := &TaskManagerOptions{}
	WithTaskTTL(0)(disabled)
	if disabled.TaskTTL != 0 {
		t.Errorf("Expected TaskTTL=0 after explicit disable, got %v", disabled.TaskTTL)
	}
	if disabled.EnableCleanup {
		t.Error("Expected WithTaskTTL(0) to leave EnableCleanup untouched")
	}
}

func stringPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}

func boolPtr(b bool) *bool {
	return &b
}

// TestTaskManager_OnListTasks covers the v1.0 ListTasks filtering and pagination.
func TestTaskManager_OnListTasks(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	ctx := context.Background()

	now := time.Now().UTC()
	seed := []protocol.Task{
		{ID: "task-a", ContextID: "ctx-1", Status: protocol.TaskStatus{
			State: protocol.TaskStateCompleted, Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339)}},
		{ID: "task-b", ContextID: "ctx-1", Status: protocol.TaskStatus{
			State: protocol.TaskStateWorking, Timestamp: now.Format(time.RFC3339)}},
		{ID: "task-c", ContextID: "ctx-2", Status: protocol.TaskStatus{
			State: protocol.TaskStateWorking, Timestamp: now.Format(time.RFC3339)},
			Artifacts: []protocol.Artifact{{ArtifactID: "art-1"}}},
	}
	manager.taskMu.Lock()
	for i := range seed {
		manager.Tasks[seed[i].ID] = NewCancellableTask(seed[i])
	}
	manager.taskMu.Unlock()

	// No filter: all tasks, sorted by ID.
	result, err := manager.OnListTasks(ctx, protocol.ListTasksParams{})
	if err != nil {
		t.Fatalf("OnListTasks failed: %v", err)
	}
	if result.TotalSize != 3 || len(result.Tasks) != 3 {
		t.Fatalf("Expected 3 tasks, got total=%d len=%d", result.TotalSize, len(result.Tasks))
	}
	if result.Tasks[0].ID != "task-a" || result.Tasks[2].ID != "task-c" {
		t.Errorf("Expected ID-sorted order, got %s..%s", result.Tasks[0].ID, result.Tasks[2].ID)
	}
	// Artifacts stripped by default.
	if result.Tasks[2].Artifacts != nil {
		t.Errorf("Expected artifacts stripped by default")
	}

	// Filter by contextId + status.
	result, err = manager.OnListTasks(ctx, protocol.ListTasksParams{
		ContextID: "ctx-1", Status: protocol.TaskStateWorking,
	})
	if err != nil {
		t.Fatalf("OnListTasks with filter failed: %v", err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID != "task-b" {
		t.Fatalf("Expected only task-b, got %+v", result.Tasks)
	}

	// IncludeArtifacts keeps artifacts.
	result, err = manager.OnListTasks(ctx, protocol.ListTasksParams{
		ContextID: "ctx-2", IncludeArtifacts: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("OnListTasks failed: %v", err)
	}
	if len(result.Tasks) != 1 || len(result.Tasks[0].Artifacts) != 1 {
		t.Fatalf("Expected task-c with artifact, got %+v", result.Tasks)
	}

	// Pagination: page size 2 -> next page token "2", second page has 1 task.
	result, err = manager.OnListTasks(ctx, protocol.ListTasksParams{PageSize: intPtr(2)})
	if err != nil {
		t.Fatalf("OnListTasks paged failed: %v", err)
	}
	if len(result.Tasks) != 2 || result.NextPageToken != "2" {
		t.Fatalf("Expected 2 tasks + token \"2\", got len=%d token=%q", len(result.Tasks), result.NextPageToken)
	}
	result, err = manager.OnListTasks(ctx, protocol.ListTasksParams{
		PageSize: intPtr(2), PageToken: result.NextPageToken,
	})
	if err != nil {
		t.Fatalf("OnListTasks page 2 failed: %v", err)
	}
	if len(result.Tasks) != 1 || result.NextPageToken != "" {
		t.Fatalf("Expected final page with 1 task, got len=%d token=%q", len(result.Tasks), result.NextPageToken)
	}
}

// TestTaskManager_PushNotificationListDelete covers the v1.0 list/delete push-config methods.
func TestTaskManager_PushNotificationListDelete(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	ctx := context.Background()

	if _, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: "task-1", URL: "https://example.com/webhook",
	}); err != nil {
		t.Fatalf("OnPushNotificationSet failed: %v", err)
	}

	list, err := manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("OnPushNotificationList failed: %v", err)
	}
	if len(list.Configs) != 1 || list.Configs[0].URL != "https://example.com/webhook" {
		t.Fatalf("Expected one config, got %+v", list.Configs)
	}

	if err := manager.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{TaskID: "task-1"}); err != nil {
		t.Fatalf("OnPushNotificationDelete failed: %v", err)
	}

	list, err = manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("OnPushNotificationList after delete failed: %v", err)
	}
	if len(list.Configs) != 0 {
		t.Fatalf("Expected empty config list after delete, got %+v", list.Configs)
	}

	// Deleting again is a no-op.
	if err := manager.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{TaskID: "task-1"}); err != nil {
		t.Fatalf("Idempotent delete failed: %v", err)
	}
}

// TestTaskManager_OnResubscribe_NonTerminalEmitsSnapshot verifies the v1.0
// requirement that subscribing to a live task delivers the current Task snapshot
// as the first stream event.
func TestTaskManager_OnResubscribe_NonTerminalEmitsSnapshot(t *testing.T) {
	manager, err := NewTaskManager(&MockMessageProcessor{})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()
	ctx := context.Background()

	task := protocol.Task{
		ID:        "live-task",
		ContextID: "ctx-1",
		Status:    protocol.TaskStatus{State: protocol.TaskStateWorking, Timestamp: time.Now().UTC().Format(time.RFC3339)},
	}
	manager.taskMu.Lock()
	manager.Tasks[task.ID] = NewCancellableTask(task)
	manager.taskMu.Unlock()

	ch, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe on live task failed: %v", err)
	}

	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed before snapshot")
		}
		if ev.GetTask() == nil {
			t.Fatalf("Expected first event to be a Task snapshot, got %+v", ev)
		}
		if ev.GetTask().ID != task.ID || ev.GetTask().Status.State != protocol.TaskStateWorking {
			t.Errorf("Snapshot mismatch: got id=%s state=%s", ev.GetTask().ID, ev.GetTask().Status.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the Task snapshot event")
	}
}

// TestTaskManager_OnResubscribe_TerminalRejected verifies the v1.0
// requirement that subscribing to a terminal task returns UnsupportedOperationError.
func TestTaskManager_OnResubscribe_TerminalRejected(t *testing.T) {
	manager, err := NewTaskManager(&MockMessageProcessor{})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()
	ctx := context.Background()

	for _, state := range []protocol.TaskState{
		protocol.TaskStateCompleted, protocol.TaskStateFailed,
		protocol.TaskStateCanceled, protocol.TaskStateRejected,
	} {
		task := protocol.Task{
			ID:     "terminal-" + string(state),
			Status: protocol.TaskStatus{State: state, Timestamp: time.Now().UTC().Format(time.RFC3339)},
		}
		manager.taskMu.Lock()
		manager.Tasks[task.ID] = NewCancellableTask(task)
		manager.taskMu.Unlock()

		ch, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: task.ID})
		if err == nil {
			t.Errorf("state %s: expected UnsupportedOperation error, got nil", state)
			continue
		}
		if ch != nil {
			t.Errorf("state %s: expected nil channel on rejection", state)
		}
		if !errors.Is(err, taskmanager.ErrUnsupportedOperationSentinel) {
			t.Errorf("state %s: expected taskmanager.ErrUnsupportedOperation, got %v", state, err)
		}
	}
}
