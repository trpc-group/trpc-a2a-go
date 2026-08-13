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
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

func TestNewTaskManager(t *testing.T) {
	tests := []struct {
		name      string
		processor taskmanager.MessageProcessor
		options   []TaskManagerOption
		wantErr   bool
	}{
		{
			name:      "valid processor",
			processor: echoExecutor(),
			wantErr:   false,
		},
		{
			name:      "nil processor",
			processor: nil,
			wantErr:   true,
		},
		{
			name:      "with options",
			processor: echoExecutor(),
			options:   []TaskManagerOption{WithMaxHistoryLength(50)},
			wantErr:   false,
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
			defer manager.Close()

			if manager == nil {
				t.Error("Expected manager but got nil")
				return
			}

			if manager.processor == nil {
				t.Error("MessageProcessor not set correctly")
			}

			if len(tt.options) > 0 && manager.options.MaxHistoryLength != 50 {
				t.Errorf("Expected MaxHistoryLength=50, got %d", manager.options.MaxHistoryLength)
			}
		})
	}
}

func TestTaskManager_OnSendMessage(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	ctx := context.Background()

	tests := []struct {
		name    string
		request protocol.SendMessageParams
	}{
		{
			name:    "valid message",
			request: userParams("Hello"),
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := manager.OnSendMessage(ctx, tt.request)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("Expected result but got nil")
				return
			}

			message := result.GetMessage()
			if message == nil {
				t.Fatalf("Expected a Message result, got %+v", result)
			}
			if message.MessageID == "" {
				t.Error("Expected message ID to be set")
			}

			// Check that the reply message is in storage
			manager.conversationMu.RLock()
			_, exists := manager.messages[newScopedID("", "", message.MessageID)]
			manager.conversationMu.RUnlock()
			if !exists {
				t.Error("Message not found in storage")
			}
		})
	}
}

func TestTaskManager_OnGetTask(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	ctx := context.Background()

	existingTaskID := "existing-task"
	seedTask(manager, protocol.Task{
		ID:        existingTaskID,
		ContextID: "ctx-existing",
		Status:    protocol.TaskStatus{State: protocol.TaskStateWorking},
	})

	tests := []struct {
		name     string
		params   protocol.TaskQueryParams
		validate func(*testing.T, *protocol.Task, error)
	}{
		{
			name:   "get existing task",
			params: protocol.TaskQueryParams{ID: existingTaskID},
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
			name:   "get non-existent task",
			params: protocol.TaskQueryParams{ID: "non-existent-task"},
			validate: func(t *testing.T, task *protocol.Task, err error) {
				if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
					t.Errorf("Expected TaskNotFound for non-existent task, got %v", err)
				}
				if task != nil {
					t.Error("Expected nil task for error case")
				}
			},
		},
		{
			name:   "empty task ID",
			params: protocol.TaskQueryParams{ID: ""},
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

func TestTaskManager_PushNotifications(t *testing.T) {
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{Sender: noopSender()}))
	ctx := context.Background()
	seedTask(manager, protocol.Task{ID: "test-task-id", Status: protocol.TaskStatus{State: protocol.TaskStateWorking}})

	tests := []struct {
		name      string
		action    string // "set" or "get"
		taskID    string
		config    *protocol.TaskPushNotificationConfig
		getParams *protocol.GetTaskPushNotificationConfigParams
		validate  func(*testing.T, interface{}, error)
	}{
		{
			name:   "set push notification",
			action: "set",
			taskID: "test-task-id",
			config: &protocol.TaskPushNotificationConfig{
				TaskID: "test-task-id",
				ID:     "cfg-set",
				URL:    "https://example.com/webhook",
				Token:  "Bearer token",
			},
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
			getParams: &protocol.GetTaskPushNotificationConfigParams{
				TaskID: "test-task-id",
				ID:     "cfg-setup",
			},
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
			getParams: &protocol.GetTaskPushNotificationConfigParams{
				TaskID: "non-existent-task",
				ID:     "cfg-missing",
			},
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
		ID:     "cfg-setup",
		URL:    "https://example.com/webhook",
		Token:  "Bearer token",
	}
	if _, err := manager.OnPushNotificationSet(ctx, setupConfig); err != nil {
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
		setup    func(*taskSubscriber)             // Setup function to perform actions
		validate func(*testing.T, *taskSubscriber) // Validation function
	}{
		{
			name:     "create subscriber",
			taskID:   "test-task",
			capacity: 5,
			setup:    func(s *taskSubscriber) {},
			validate: func(t *testing.T, s *taskSubscriber) {
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
			setup: func(s *taskSubscriber) {
				event := protocol.NewStreamResponseMessage(agentReply("Test event"))
				if err := s.Send(event); err != nil {
					t.Errorf("Unexpected error sending event: %v", err)
				}
			},
			validate: func(t *testing.T, s *taskSubscriber) {
				select {
				case receivedEvent := <-s.Channel():
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
			setup: func(s *taskSubscriber) {
				s.Close()
			},
			validate: func(t *testing.T, s *taskSubscriber) {
				if !s.Closed() {
					t.Error("Expected subscriber to be closed")
				}

				// Test sending to closed subscriber
				event := protocol.NewStreamResponseMessage(&protocol.Message{Role: protocol.MessageRoleAgent})
				if err := s.Send(event); err == nil {
					t.Error("Expected error when sending to closed subscriber")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subscriber := newTaskSubscriber(tt.taskID, tt.capacity, false)

			tt.setup(subscriber)
			tt.validate(t, subscriber)
		})
	}
}

func TestTaskSubscriber_CloseUnblocksBlockingSend(t *testing.T) {
	subscriber := newTaskSubscriber("blocking-send-task", 1, true)

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

func TestTaskManager_cleanupFailedSubscribersClosesRemovedSubscribers(t *testing.T) {
	manager := newTestManager(t, echoExecutor())

	taskID := "failed-subscriber-task"
	failedSub := newTaskSubscriber(taskID, 10, false)
	activeSub := newTaskSubscriber(taskID, 10, false)

	manager.taskMu.Lock()
	manager.subscribers[newScopedID("", "", taskID)] = []*taskSubscriber{failedSub, activeSub}
	manager.taskMu.Unlock()

	manager.cleanupFailedSubscribers("", "", taskID, []*taskSubscriber{failedSub})

	if !failedSub.Closed() {
		t.Error("Expected failed subscriber to be closed")
	}
	if activeSub.Closed() {
		t.Error("Expected active subscriber to remain open")
	}

	manager.taskMu.RLock()
	subs := manager.subscribers[newScopedID("", "", taskID)]
	manager.taskMu.RUnlock()

	if len(subs) != 1 || subs[0] != activeSub {
		t.Fatalf("Expected only active subscriber to remain, got %d subscribers", len(subs))
	}
}

func TestTaskManager_cleanExpiredTasks(t *testing.T) {
	manager := newTestManager(t, echoExecutor())

	// Create a task in a final state with an old timestamp
	seedTask(manager, protocol.Task{
		ID: "expired-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCompleted,
			Timestamp: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		},
	})
	manager.taskMu.Lock()
	manager.subscribers[newScopedID("", "", "expired-task")] = []*taskSubscriber{
		newTaskSubscriber("expired-task", 10, false),
	}
	manager.taskMu.Unlock()

	// Create a non-expired task
	seedTask(manager, protocol.Task{
		ID: "active-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateWorking,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	})

	// Create a recently completed task (should NOT be cleaned)
	seedTask(manager, protocol.Task{
		ID: "recent-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCompleted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	})

	// TTL=0 should skip cleanup entirely
	skipped := manager.cleanExpiredTasks(0)
	if skipped != 0 {
		t.Errorf("Expected 0 cleaned tasks with TTL=0, got %d", skipped)
	}
	manager.taskMu.RLock()
	if _, exists := manager.tasks[newScopedID("", "", "expired-task")]; !exists {
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

	if _, exists := manager.tasks[newScopedID("", "", "expired-task")]; exists {
		t.Error("Expected expired task to be removed")
	}
	if _, exists := manager.subscribers[newScopedID("", "", "expired-task")]; exists {
		t.Error("Expected expired task subscribers to be removed")
	}
	if _, exists := manager.tasks[newScopedID("", "", "active-task")]; !exists {
		t.Error("Active task should not be removed")
	}
	if _, exists := manager.tasks[newScopedID("", "", "recent-task")]; !exists {
		t.Error("Recently completed task should not be removed")
	}
}

func TestTaskManager_TaskTTLCleanupGoroutine(t *testing.T) {
	manager := newTestManager(
		t,
		echoExecutor(),
		WithConversationTTL(time.Hour, 5*time.Millisecond),
		WithTaskTTL(time.Nanosecond),
	)

	taskID := "auto-expired-task"
	sub := newTaskSubscriber(taskID, 10, false)
	seedTask(manager, protocol.Task{
		ID: taskID,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCompleted,
			Timestamp: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	})

	manager.taskMu.Lock()
	manager.subscribers[newScopedID("", "", taskID)] = []*taskSubscriber{sub}
	manager.taskMu.Unlock()

	if _, err := manager.pushStore.save("", protocol.TaskPushNotificationConfig{TaskID: taskID}); err != nil {
		t.Fatalf("Failed to seed push config: %v", err)
	}

	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		manager.taskMu.RLock()
		_, taskExists := manager.tasks[newScopedID("", "", taskID)]
		_, subsExists := manager.subscribers[newScopedID("", "", taskID)]
		manager.taskMu.RUnlock()

		pushConfigs := manager.pushStore.list("", "", taskID)
		pushExists := len(pushConfigs) > 0

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
	manager, err := NewTaskManager(echoExecutor())
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Add some tasks and subscribers
	seedTask(manager, protocol.Task{
		ID: "close-test-task",
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateWorking,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	})
	sub := newTaskSubscriber("close-test-task", 10, false)

	manager.taskMu.Lock()
	manager.subscribers[newScopedID("", "", "close-test-task")] = []*taskSubscriber{sub}
	manager.taskMu.Unlock()

	manager.Close()

	if !sub.Closed() {
		t.Error("Expected subscriber to be closed after manager.Close()")
	}

	manager.taskMu.RLock()
	defer manager.taskMu.RUnlock()

	if len(manager.tasks) != 0 {
		t.Errorf("Expected all tasks to be cleaned, got %d", len(manager.tasks))
	}
	if len(manager.subscribers) != 0 {
		t.Errorf("Expected all subscribers to be cleaned, got %d", len(manager.subscribers))
	}
}

func TestTaskManager_Close_Idempotent(t *testing.T) {
	manager, err := NewTaskManager(echoExecutor())
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
	manager := newTestManager(t, echoExecutor())
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
	for i := range seed {
		seedTask(manager, seed[i])
	}

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
	manager := newTestManager(t, echoExecutor(), WithPushNotifications(push.Config{Sender: noopSender()}))
	ctx := context.Background()
	seedTask(manager, protocol.Task{ID: "task-1", Status: protocol.TaskStatus{State: protocol.TaskStateWorking}})

	created, err := manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		TaskID: "task-1", URL: "https://example.com/webhook",
	})
	if err != nil {
		t.Fatalf("OnPushNotificationSet failed: %v", err)
	}

	list, err := manager.OnPushNotificationList(ctx, protocol.ListTaskPushNotificationConfigsParams{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("OnPushNotificationList failed: %v", err)
	}
	if len(list.Configs) != 1 || list.Configs[0].URL != "https://example.com/webhook" {
		t.Fatalf("Expected one config, got %+v", list.Configs)
	}

	if err := manager.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{
		TaskID: "task-1", ID: created.ID,
	}); err != nil {
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
	if err := manager.OnPushNotificationDelete(ctx, protocol.DeleteTaskPushNotificationConfigParams{
		TaskID: "task-1", ID: created.ID,
	}); err != nil {
		t.Fatalf("Idempotent delete failed: %v", err)
	}
}

// TestTaskManager_OnResubscribe_NonTerminalEmitsSnapshot verifies the v1.0
// requirement that subscribing to a live task delivers the current Task snapshot
// as the first stream event.
func TestTaskManager_OnResubscribe_NonTerminalEmitsSnapshot(t *testing.T) {
	manager := newTestManager(t, echoExecutor())
	ctx := context.Background()

	task := protocol.Task{
		ID:        "live-task",
		ContextID: "ctx-1",
		Status:    protocol.TaskStatus{State: protocol.TaskStateWorking, Timestamp: time.Now().UTC().Format(time.RFC3339)},
	}
	seedTask(manager, task)

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
	manager := newTestManager(t, echoExecutor())
	ctx := context.Background()

	for _, state := range []protocol.TaskState{
		protocol.TaskStateCompleted, protocol.TaskStateFailed,
		protocol.TaskStateCanceled, protocol.TaskStateRejected,
	} {
		task := protocol.Task{
			ID:     "terminal-" + string(state),
			Status: protocol.TaskStatus{State: state, Timestamp: time.Now().UTC().Format(time.RFC3339)},
		}
		seedTask(manager, task)

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
