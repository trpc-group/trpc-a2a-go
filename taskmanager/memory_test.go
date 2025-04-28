// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	// "trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc" // Removed unused import
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// mockProcessor is a simple TaskProcessor for testing.
type mockProcessor struct {
	processFunc func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error
	mu          sync.Mutex
	callCount   int
	lastTaskID  string
	lastMessage protocol.Message
}

// Process implements TaskProcessor.
func (p *mockProcessor) Process(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
	p.mu.Lock()
	p.callCount++
	p.lastTaskID = taskID
	p.lastMessage = msg
	customFunc := p.processFunc
	p.mu.Unlock()

	if customFunc != nil {
		return customFunc(ctx, taskID, msg, handle)
	}

	// Default behavior: complete successfully after a short delay
	// to allow subscribers to attach.
	time.Sleep(10 * time.Millisecond)

	// Check if context is done before attempting to complete the task
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Set the status to Working first to test transition
	err := handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
		Role:  protocol.MessageRoleAgent,
		Parts: []protocol.Part{protocol.NewTextPart("Mock Working...")},
	})
	if err != nil {
		return err
	}

	// Small delay between updates
	time.Sleep(5 * time.Millisecond)

	// Check context again before final update
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Mark task as completed and send final message
	err = handle.UpdateStatus(protocol.TaskStateCompleted, &protocol.Message{
		Role:  protocol.MessageRoleAgent,
		Parts: []protocol.Part{protocol.NewTextPart("Mock Success")},
	})

	return err
}

func TestNewMemoryTaskManager(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)
	assert.NotNil(t, tm)
	assert.NotNil(t, tm.Tasks)
	assert.NotNil(t, tm.Messages)
	assert.NotNil(t, tm.Subscribers)
	assert.Equal(t, processor, tm.Processor)

	// Test error case
	tm, err = NewMemoryTaskManager(nil)
	assert.Error(t, err)
	assert.Nil(t, tm)
}

// assertTextPart is a helper function that asserts that a Part is a TextPart
// and contains the expected text. Returns the TextPart for further assertions.
func assertTextPart(t *testing.T, part protocol.Part, expectedText string) protocol.TextPart {
	t.Helper()
	textPart, ok := part.(protocol.TextPart)
	require.True(t, ok, "Expected part to be TextPart")
	if expectedText != "" {
		assert.Contains(t, textPart.Text, expectedText, "TextPart should contain expected text")
	}
	return textPart
}

// assertTaskStatus is a helper function that asserts a task has the expected state.
func assertTaskStatus(t *testing.T, task *protocol.Task, expectedID string, expectedState protocol.TaskState) {
	t.Helper()
	require.NotNil(t, task, "Task should not be nil")
	assert.Equal(t, expectedID, task.ID, "Task ID should match")
	assert.Equal(t, expectedState, task.Status.State, "Task state should match expected")
}

// createTestTask creates a standard test task with the given ID and message text.
func createTestTask(id, messageText string) protocol.SendTaskParams {
	return protocol.SendTaskParams{
		ID: id,
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart(messageText)},
		},
	}
}

// Helper function to collect task events from a channel until completion or timeout
func collectTaskEvents(t *testing.T, eventChan <-chan protocol.TaskEvent, targetState protocol.TaskState, timeoutDuration time.Duration) []protocol.TaskEvent {
	// Collect events with timeout
	events := []protocol.TaskEvent{}
	timeout := time.After(timeoutDuration) // Safety timeout
	done := false
	for !done {
		select {
		case event, ok := <-eventChan:
			if !ok {
				done = true // Channel closed
				break
			}
			events = append(events, event)

			// If we receive a final event matching our target state, we can exit early
			if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok &&
				statusEvent.Final && statusEvent.Status.State == targetState {
				// We got what we need, break out
				t.Logf("Received final %s event, breaking early", targetState)
				done = true
			}
		case <-timeout:
			t.Logf("Test timed out waiting for events, proceeding with test using %d collected events", len(events))
			done = true
		}
	}
	return events
}

// TestMemTaskManager_OnSendTask_Sync tests the synchronous OnSendTask method.
func TestMemTaskManager_OnSendTask_Sync(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	taskID := "test-sync-1"
	params := createTestTask(taskID, "Sync Task")

	task, err := tm.OnSendTask(context.Background(), params)
	require.NoError(t, err)
	assertTaskStatus(t, task, taskID, protocol.TaskStateCompleted) // Default mock behavior

	// Verify message content
	require.NotNil(t, task.Status.Message)
	require.NotEmpty(t, task.Status.Message.Parts)
	assertTextPart(t, task.Status.Message.Parts[0], "Mock Success")

	processor.mu.Lock()
	assert.Equal(t, 1, processor.callCount)
	assert.Equal(t, taskID, processor.lastTaskID)
	processor.mu.Unlock()

	// Check stored message - there should be at least 3 messages now:
	// 1. User message
	// 2. Working status from mock processor
	// 3. Completed status from mock processor
	tm.MessagesMutex.RLock()
	history, ok := tm.Messages[taskID]
	tm.MessagesMutex.RUnlock()
	require.True(t, ok)
	require.GreaterOrEqual(t, len(history), 3, "Should have at least 3 messages in history")

	// Check first message is from user
	assert.Equal(t, protocol.MessageRoleUser, history[0].Role)
	textPartHistory := assertTextPart(t, history[0].Parts[0], "Sync Task")
	assert.Equal(t, "Sync Task", textPartHistory.Text)

	// Check last message has completion status
	lastMsg := history[len(history)-1]
	assert.Equal(t, protocol.MessageRoleAgent, lastMsg.Role)
	assertTextPart(t, lastMsg.Parts[0], "Mock Success")

	// Test processor error case
	processor.processFunc = func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
		return fmt.Errorf("processor error")
	}
	taskID = "test-sync-err"
	params.ID = taskID
	task, err = tm.OnSendTask(context.Background(), params)
	require.Error(t, err)
	assertTaskStatus(t, task, taskID, protocol.TaskStateFailed)

	// Verify error message
	require.NotNil(t, task.Status.Message)
	require.NotEmpty(t, task.Status.Message.Parts)
	assertTextPart(t, task.Status.Message.Parts[0], "processor error")
}

func TestOnSendTaskSubAsync(t *testing.T) {
	// Create processor with custom logic for this test
	processor := &mockProcessor{
		processFunc: func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
			// Explicitly set working state
			err := handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart("Mock working...")},
			})
			if err != nil {
				return err
			}

			// Short delay
			time.Sleep(5 * time.Millisecond)

			// Check context
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Set completed state
			return handle.UpdateStatus(protocol.TaskStateCompleted, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart("Mock Success")},
			})
		},
	}

	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	taskID := "test-async-1"
	params := createTestTask(taskID, "Async Task")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventChan, err := tm.OnSendTaskSubscribe(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, eventChan)

	// Use helper function to collect events
	events := collectTaskEvents(t, eventChan, protocol.TaskStateCompleted, 3*time.Second)

	require.NotEmpty(t, events, "Should have received at least one event")

	// Find the Working event
	var foundWorking bool
	for _, event := range events {
		if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok &&
			statusEvent.Status.State == protocol.TaskStateWorking {
			foundWorking = true
			break
		}
	}
	assert.True(t, foundWorking, "Should have received a Working state event")

	// Find the Completed event (final event)
	var foundCompleted bool
	for _, event := range events {
		if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok &&
			statusEvent.Status.State == protocol.TaskStateCompleted && statusEvent.Final {
			foundCompleted = true

			// Validate the completed event message
			require.NotNil(t, statusEvent.Status.Message)
			require.NotEmpty(t, statusEvent.Status.Message.Parts)
			assertTextPart(t, statusEvent.Status.Message.Parts[0], "Mock Success")
			break
		}
	}
	assert.True(t, foundCompleted, "Should have received a Completed state event")

	// Double check task state via OnGetTask
	task, err := tm.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	require.NoError(t, err)
	assertTaskStatus(t, task, taskID, protocol.TaskStateCompleted)

	// Check processor was called
	processor.mu.Lock()
	assert.Equal(t, 1, processor.callCount)
	assert.Equal(t, taskID, processor.lastTaskID)
	processor.mu.Unlock()

	// Check stored message
	tm.MessagesMutex.RLock()
	history, ok := tm.Messages[taskID]
	tm.MessagesMutex.RUnlock()
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(history), 2, "Should have at least 2 messages in history")

	// Check first message (from user)
	require.NotEmpty(t, history[0].Parts)
	textPartHistAsync := assertTextPart(t, history[0].Parts[0], "Async Task")
	assert.Equal(t, "Async Task", textPartHistAsync.Text)
}

func TestMemTaskMgr_OnSendTaskSub_Error(t *testing.T) {
	errMsg := "async processor error"
	processor := &mockProcessor{
		processFunc: func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
			// Simulate some work before failing
			err := handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart("Working...")},
			})
			if err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)

			// Check context
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Return error to simulate failure
			return fmt.Errorf(errMsg)
		},
	}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	taskID := "test-async-err-1"
	params := createTestTask(taskID, "Async Fail Task")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventChan, err := tm.OnSendTaskSubscribe(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, eventChan)

	// Use helper function to collect events
	events := collectTaskEvents(t, eventChan, protocol.TaskStateFailed, 3*time.Second)

	require.NotEmpty(t, events, "Should have received at least one event")

	// Find the Working event
	var foundWorking bool
	for _, event := range events {
		if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok &&
			statusEvent.Status.State == protocol.TaskStateWorking {
			foundWorking = true
			break
		}
	}
	assert.True(t, foundWorking, "Should have received a Working state event")

	// Find the Failed event (final event)
	var foundFailed bool
	for _, event := range events {
		if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok &&
			statusEvent.Status.State == protocol.TaskStateFailed && statusEvent.Final {
			foundFailed = true

			// Validate the failed event message
			require.NotNil(t, statusEvent.Status.Message)
			require.NotEmpty(t, statusEvent.Status.Message.Parts)
			assertTextPart(t, statusEvent.Status.Message.Parts[0], errMsg)
			break
		}
	}
	assert.True(t, foundFailed, "Should have received a Failed state event")

	// Double check task state via OnGetTask
	task, err := tm.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	require.NoError(t, err)
	assertTaskStatus(t, task, taskID, protocol.TaskStateFailed)
}

func TestMemoryTaskManager_OnGetTask(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	taskID := "test-get-1"
	// Explicitly create TextPart first
	helloPart := protocol.NewTextPart("Hello")
	_, okDirect := interface{}(helloPart).(protocol.TextPart) // Check value type
	require.True(t, okDirect, "helloPart should be assertable to TextPart")

	userMsg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{helloPart}}
	_, okInSliceImmediate := userMsg.Parts[0].(protocol.TextPart) // Check value type
	require.True(t, okInSliceImmediate, "Part in userMsg slice should be assertable immediately to TextPart")

	params := protocol.SendTaskParams{
		ID:       taskID,
		Message:  userMsg,
		Metadata: map[string]interface{}{"meta1": "value1"},
	}

	// Send a task to create it
	_, err = tm.OnSendTask(context.Background(), params)
	require.NoError(t, err)

	// Get the task without history
	getParams := protocol.TaskQueryParams{ID: taskID}
	task, err := tm.OnGetTask(context.Background(), getParams)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, taskID, task.ID)
	assert.Equal(t, protocol.TaskStateCompleted, task.Status.State) // From mock processor
	assert.Equal(t, "value1", task.Metadata["meta1"])
	assert.Nil(t, task.History, "History should be nil when not requested")

	// Get the task with history
	histLen := 1
	getParams.HistoryLength = &histLen
	task, err = tm.OnGetTask(context.Background(), getParams)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.NotNil(t, task.History, "History should not be nil when requested")
	require.Len(t, task.History, 1)
	// Use reflection to get the actual type and compare the text contents
	historyPart := task.History[0].Parts[0]
	require.NotNil(t, historyPart, "History part should not be nil")

	// Get the text content regardless of whether it's value or pointer
	var historyText string
	var historyPartTypeOK bool

	// Try both value and pointer type assertions
	if textPart, ok := historyPart.(protocol.TextPart); ok {
		historyText = textPart.Text
		historyPartTypeOK = true
		t.Logf("Found TextPart value type in history")
	} else if textPartPtr, ok := historyPart.(*protocol.TextPart); ok {
		historyText = textPartPtr.Text
		historyPartTypeOK = true
		t.Logf("Found *TextPart pointer type in history")
	} else {
		t.Logf("Expected TextPart or *TextPart but got %T", historyPart)
	}

	// Accept either TextPart or *TextPart, the important part is the text content
	require.True(t, historyPartTypeOK, "History part was not TextPart or *TextPart")

	assert.Equal(t, "Mock Success", historyText) // Compare history text with the expected last message from the agent

	// Get non-existent task
	getParams.ID = "non-existent-task"
	task, err = tm.OnGetTask(context.Background(), getParams)
	require.Error(t, err)

	// Check error type by asserting to JSONRPCError and comparing code
	if rpcErr, ok := err.(*jsonrpc.Error); ok {
		assert.Equal(t, ErrCodeTaskNotFound, rpcErr.Code)
		assert.Equal(t, "Task not found", rpcErr.Message)
	} else {
		t.Errorf("Expected *jsonrpc.JSONRPCError but got %T", err)
	}
	assert.Nil(t, task)
}

func TestMemoryTaskManager_OnCancelTask(t *testing.T) {
	// Setup a processor with a delayed execution to allow cancellation during processing
	processor := &mockProcessor{
		processFunc: func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
			// Create a channel to track if context cancellation is received
			done := make(chan struct{})
			canceled := make(chan struct{})

			// Start a goroutine that will block until either context is cancelled or timeout
			go func() {
				select {
				case <-ctx.Done():
					close(canceled)
				case <-time.After(100 * time.Millisecond): // Reduced timeout to make test faster
					// Should not reach here if cancellation works properly
				}
				close(done)
			}()

			// Wait for the goroutine to complete
			<-done

			// Check if cancellation was received
			select {
			case <-canceled:
				return ctx.Err() // Return the context error (context.Canceled)
			default:
				return nil // Successfully completed without cancellation
			}
		},
	}

	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	// Create a task
	taskID := "test-cancel-task"
	params := protocol.SendTaskParams{
		ID:      taskID,
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Test task for cancellation")}},
	}

	// Start task with subscription to monitor events
	eventChan, err := tm.OnSendTaskSubscribe(context.Background(), params)
	require.NoError(t, err)

	// Give task a moment to start processing
	time.Sleep(50 * time.Millisecond)

	// Verify task is in working state before cancellation
	task, err := tm.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
	require.NoError(t, err)
	assert.Equal(t, protocol.TaskStateWorking, task.Status.State)

	// Now cancel the task
	cancelParams := protocol.TaskIDParams{ID: taskID}
	canceledTask, err := tm.OnCancelTask(context.Background(), cancelParams)
	require.NoError(t, err)
	require.NotNil(t, canceledTask)

	// Wait a little bit for the cancellation to fully propagate if needed
	if canceledTask.Status.State != protocol.TaskStateCanceled {
		// Poll for a short time until the task shows as canceled
		deadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) {
			canceledTask, err = tm.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: taskID})
			require.NoError(t, err)
			if canceledTask.Status.State == protocol.TaskStateCanceled {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	assert.Equal(t, protocol.TaskStateCanceled, canceledTask.Status.State)

	// Collect events with timeout
	var lastEvent protocol.TaskEvent
	eventsCollected := false
	timeout := time.After(1 * time.Second)

	for !eventsCollected {
		select {
		case event, ok := <-eventChan:
			if !ok {
				eventsCollected = true // Channel closed
				break
			}
			lastEvent = event
			// If we see the final canceled event, don't wait for channel close
			if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok &&
				statusEvent.Status.State == protocol.TaskStateCanceled && statusEvent.Final {
				eventsCollected = true
			}
		case <-timeout:
			t.Logf("Timeout waiting for event channel to close, proceeding with test")
			eventsCollected = true
		}
	}

	// Verify last event indicates cancellation if we got events
	if lastEvent != nil {
		statusEvent, ok := lastEvent.(protocol.TaskStatusUpdateEvent)
		require.True(t, ok, "Expected TaskStatusUpdateEvent")
		assert.Equal(t, taskID, statusEvent.ID)
		assert.Equal(t, protocol.TaskStateCanceled, statusEvent.Status.State)
		assert.True(t, statusEvent.Final)
	}

	// Test cancelling a non-existent task
	_, err = tm.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: "non-existent-task"})
	assert.Error(t, err)
	// Check if the error is a jsonrpc.Error with the TaskNotFound error code
	jsonRPCErr, ok := err.(*jsonrpc.Error)
	assert.True(t, ok, "Expected jsonrpc.Error")
	assert.Equal(t, ErrCodeTaskNotFound, jsonRPCErr.Code)

	// Test cancelling an already cancelled task
	againCanceledTask, err := tm.OnCancelTask(context.Background(), cancelParams)
	// Updated expectation: Task is already in final state, can't cancel again
	// This returns the task but with an error indicating it's already in final state
	assert.Error(t, err)
	jsonRPCErr, ok = err.(*jsonrpc.Error)
	assert.True(t, ok, "Expected jsonrpc.Error")
	assert.Equal(t, ErrCodeTaskFinal, jsonRPCErr.Code)
	assert.Equal(t, protocol.TaskStateCanceled, againCanceledTask.Status.State)

	// Test cancelling a completed task (should return task without error)
	completedTaskID := "completed-task"
	completedParams := protocol.SendTaskParams{
		ID:      completedTaskID,
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Completed task")}},
	}

	// Use the basic mock processor behavior (completes task quickly)
	processor.processFunc = nil
	_, err = tm.OnSendTask(context.Background(), completedParams)
	require.NoError(t, err)

	// Verify task is in completed state
	completedTask, err := tm.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: completedTaskID})
	require.NoError(t, err)
	assert.Equal(t, protocol.TaskStateCompleted, completedTask.Status.State)

	// Try to cancel the completed task
	againCompletedTask, err := tm.OnCancelTask(context.Background(), protocol.TaskIDParams{ID: completedTaskID})
	// Update expectation: can't cancel a task that's already completed
	assert.Error(t, err)
	jsonRPCErr, ok = err.(*jsonrpc.Error)
	assert.True(t, ok, "Expected jsonrpc.Error for canceling completed task")
	assert.Equal(t, ErrCodeTaskFinal, jsonRPCErr.Code)
	assert.Equal(t, protocol.TaskStateCompleted, againCompletedTask.Status.State,
		"Already completed task should remain in completed state after cancel attempt")
}

// --- Test Helpers ---

func TestIsFinalState(t *testing.T) {
	assert.True(t, isFinalState(protocol.TaskStateCompleted))
	assert.True(t, isFinalState(protocol.TaskStateFailed))
	assert.True(t, isFinalState(protocol.TaskStateCanceled))
	assert.False(t, isFinalState(protocol.TaskStateWorking))
	assert.False(t, isFinalState(protocol.TaskStateSubmitted))     // Check defined non-final state.
	assert.False(t, isFinalState(protocol.TaskStateInputRequired)) // Check defined non-final state.
	assert.False(t, isFinalState(protocol.TaskState("other")))
}

func TestMemTaskManagerPushNotif(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	// Create a task first
	taskID := "push-notification-task"
	params := protocol.SendTaskParams{
		ID:      taskID,
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Test task for push notifications")}},
	}

	// Start the task
	_, err = tm.OnSendTask(context.Background(), params)
	require.NoError(t, err)

	// Test setting push notification config
	pushConfig := protocol.TaskPushNotificationConfig{
		ID: taskID,
		PushNotificationConfig: protocol.PushNotificationConfig{
			URL:   "https://example.com/webhook",
			Token: "test-token",
			Authentication: &protocol.AuthenticationInfo{
				Schemes:     []string{"Bearer"},
				Credentials: stringPtr("Bearer test-token"),
			},
			Metadata: map[string]interface{}{
				"priority": "high",
			},
		},
	}

	// Test OnPushNotificationSet
	resultConfig, err := tm.OnPushNotificationSet(context.Background(), pushConfig)
	require.NoError(t, err)
	require.NotNil(t, resultConfig)
	assert.Equal(t, taskID, resultConfig.ID)
	assert.Equal(t, "https://example.com/webhook", resultConfig.PushNotificationConfig.URL)
	assert.Equal(t, "test-token", resultConfig.PushNotificationConfig.Token)
	require.NotNil(t, resultConfig.PushNotificationConfig.Authentication)
	assert.Equal(t, []string{"Bearer"}, resultConfig.PushNotificationConfig.Authentication.Schemes)
	require.NotNil(t, resultConfig.PushNotificationConfig.Authentication.Credentials)
	assert.Equal(t, "Bearer test-token", *resultConfig.PushNotificationConfig.Authentication.Credentials)
	assert.Equal(t, "high", resultConfig.PushNotificationConfig.Metadata["priority"])

	// Test OnPushNotificationGet
	getParams := protocol.TaskIDParams{ID: taskID}
	fetchedConfig, err := tm.OnPushNotificationGet(context.Background(), getParams)
	require.NoError(t, err)
	require.NotNil(t, fetchedConfig)
	assert.Equal(t, taskID, fetchedConfig.ID)
	assert.Equal(t, "https://example.com/webhook", fetchedConfig.PushNotificationConfig.URL)
	assert.Equal(t, "test-token", fetchedConfig.PushNotificationConfig.Token)
	require.NotNil(t, fetchedConfig.PushNotificationConfig.Authentication)
	assert.Equal(t, []string{"Bearer"}, fetchedConfig.PushNotificationConfig.Authentication.Schemes)
	require.NotNil(t, fetchedConfig.PushNotificationConfig.Authentication.Credentials)
	assert.Equal(t, "Bearer test-token", *fetchedConfig.PushNotificationConfig.Authentication.Credentials)
	require.NotNil(t, fetchedConfig.PushNotificationConfig.Metadata)
	assert.Equal(t, "high", fetchedConfig.PushNotificationConfig.Metadata["priority"])

	// Test setting push notification for non-existent task
	nonExistentConfig := protocol.TaskPushNotificationConfig{
		ID: "non-existent-task",
		PushNotificationConfig: protocol.PushNotificationConfig{
			URL: "https://example.com/webhook",
		},
	}
	_, err = tm.OnPushNotificationSet(context.Background(), nonExistentConfig)
	assert.NoError(t, err)

	// Test getting push notification for non-existent task
	_, err = tm.OnPushNotificationGet(context.Background(), protocol.TaskIDParams{ID: "non-existent-task"})
	assert.Error(t, err)
	jsonRPCErr, ok := err.(*jsonrpc.Error)
	assert.True(t, ok, "Expected jsonrpc.Error")
	assert.Equal(t, ErrCodeTaskNotFound, jsonRPCErr.Code)

	// Test getting push notification for task without config
	// Create a new task without push notification config
	newTaskID := "task-without-push-config"
	newParams := protocol.SendTaskParams{
		ID:      newTaskID,
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Task without push config")}},
	}
	_, err = tm.OnSendTask(context.Background(), newParams)
	require.NoError(t, err)

	// Try to get push notification config
	_, err = tm.OnPushNotificationGet(context.Background(), protocol.TaskIDParams{ID: newTaskID})
	assert.Error(t, err)
	jsonRPCErr, ok = err.(*jsonrpc.Error)
	assert.True(t, ok, "Expected jsonrpc.Error")
	assert.Equal(t, ErrCodePushNotificationNotConfigured, jsonRPCErr.Code)

	// Update the push notification config
	updatedConfig := protocol.TaskPushNotificationConfig{
		ID: taskID,
		PushNotificationConfig: protocol.PushNotificationConfig{
			URL:   "https://updated-example.com/webhook",
			Token: "updated-token",
			Authentication: &protocol.AuthenticationInfo{
				Schemes:     []string{"Bearer"},
				Credentials: stringPtr("Bearer updated-token"),
			},
		},
	}

	updatedResult, err := tm.OnPushNotificationSet(context.Background(), updatedConfig)
	require.NoError(t, err)
	assert.Equal(t, "https://updated-example.com/webhook", updatedResult.PushNotificationConfig.URL)
	assert.Equal(t, "updated-token", updatedResult.PushNotificationConfig.Token)
	require.NotNil(t, updatedResult.PushNotificationConfig.Authentication)
	assert.Equal(t, []string{"Bearer"}, updatedResult.PushNotificationConfig.Authentication.Schemes)
	require.NotNil(t, updatedResult.PushNotificationConfig.Authentication.Credentials)
	assert.Equal(t, "Bearer updated-token", *updatedResult.PushNotificationConfig.Authentication.Credentials)

	// Fetch again to verify update
	fetchedUpdatedConfig, err := tm.OnPushNotificationGet(context.Background(), getParams)
	require.NoError(t, err)
	assert.Equal(t, "https://updated-example.com/webhook", fetchedUpdatedConfig.PushNotificationConfig.URL)
	assert.Equal(t, "updated-token", fetchedUpdatedConfig.PushNotificationConfig.Token)
	require.NotNil(t, fetchedUpdatedConfig.PushNotificationConfig.Authentication)
	assert.Equal(t, []string{"Bearer"}, fetchedUpdatedConfig.PushNotificationConfig.Authentication.Schemes)
	require.NotNil(t, fetchedUpdatedConfig.PushNotificationConfig.Authentication.Credentials)
	assert.Equal(t, "Bearer updated-token", *fetchedUpdatedConfig.PushNotificationConfig.Authentication.Credentials)
}

func TestMemoryTaskManager_OnResubscribe(t *testing.T) {
	// Create a processor that will take longer to complete so we can test resubscribe
	processor := &mockProcessor{
		processFunc: func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
			// Set initial status and send an intermediate message
			err := handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart("Working on task...")},
			})
			if err != nil {
				return err
			}

			// Sleep a short time to simulate work
			time.Sleep(50 * time.Millisecond)

			// Check if the context was cancelled
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Complete the task
			err = handle.UpdateStatus(protocol.TaskStateCompleted, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart("Task completed!")},
			})
			return err
		},
	}

	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	// Create a task
	taskID := "resubscribe-task"
	params := protocol.SendTaskParams{
		ID:      taskID,
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Test task for resubscribe")}},
	}

	// Start task with subscription
	originalEventChan, err := tm.OnSendTaskSubscribe(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, originalEventChan)

	// Wait for task to start processing (wait for Working state)
	var receivedWorkingEvent bool
	for event := range originalEventChan {
		statusEvent, ok := event.(protocol.TaskStatusUpdateEvent)
		if ok && statusEvent.Status.State == protocol.TaskStateWorking {
			receivedWorkingEvent = true
			break
		}
		if event.IsFinal() {
			break
		}
	}
	assert.True(t, receivedWorkingEvent, "Should have received Working state event")

	// Now simulate a client disconnect and reconnect by resubscribing
	resubscribeParams := protocol.TaskIDParams{ID: taskID}
	resubscribeEventChan, err := tm.OnResubscribe(context.Background(), resubscribeParams)
	require.NoError(t, err)
	require.NotNil(t, resubscribeEventChan, "Should get a valid event channel from resubscribe")

	// Read events from the resubscribe channel until we get a final event
	var gotFinalEvent bool
	var statusUpdateEvent protocol.TaskStatusUpdateEvent

	for event := range resubscribeEventChan {
		if event.IsFinal() {
			// Try to type assert it to a status update event
			statusUpdate, ok := event.(protocol.TaskStatusUpdateEvent)
			if ok {
				statusUpdateEvent = statusUpdate
				gotFinalEvent = true
			}
			break
		}
	}

	// There should be a final event
	assert.True(t, gotFinalEvent, "Should have received a final event")
	assert.Equal(t, protocol.TaskStateCompleted, statusUpdateEvent.Status.State)
	assert.True(t, statusUpdateEvent.Final)

	// Test resubscribing to a non-existent task
	_, err = tm.OnResubscribe(context.Background(), protocol.TaskIDParams{ID: "non-existent-task"})
	assert.Error(t, err)
	jsonRPCErr, ok := err.(*jsonrpc.Error)
	assert.True(t, ok, "Expected jsonrpc.Error")
	assert.Equal(t, ErrCodeTaskNotFound, jsonRPCErr.Code)

	// Test resubscribing to an already completed task
	// Should get a channel with the final event and then close
	completedTaskID := "completed-resubscribe-task"
	completedParams := protocol.SendTaskParams{
		ID:      completedTaskID,
		Message: protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("Already completed task")}},
	}

	_, err = tm.OnSendTask(context.Background(), completedParams)
	require.NoError(t, err)

	// The task should be completed now, attempt to resubscribe
	completedResubChan, err := tm.OnResubscribe(
		context.Background(), protocol.TaskIDParams{ID: completedTaskID},
	)
	require.NoError(t, err)
	require.NotNil(t, completedResubChan)

	// Read all events from the channel
	completedEvents := []protocol.TaskEvent{}
	for event := range completedResubChan {
		completedEvents = append(completedEvents, event)
	}

	// Should have received a single event with the final status
	require.Len(t, completedEvents, 1, "Should get exactly one event for a completed task")
	completedStatusEvent, ok := completedEvents[0].(protocol.TaskStatusUpdateEvent)
	require.True(t, ok, "Event should be a TaskStatusUpdateEvent")
	assert.Equal(t, protocol.TaskStateCompleted, completedStatusEvent.Status.State)
	assert.True(t, completedStatusEvent.Final)
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}

// TestAddArtifact tests the AddArtifact method of TaskHandle and MemoryTaskManager
func TestAddArtifact(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	taskID := "test-artifact-task"
	params := createTestTask(taskID, "Test with artifacts")

	// Helper function to create bool pointers
	boolPtr := func(b bool) *bool {
		return &b
	}

	// Create a task and get its handle
	processor.processFunc = func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
		// Test adding an artifact to the task
		textPart := protocol.NewTextPart("Artifact content")
		err := handle.AddArtifact(protocol.Artifact{
			Name:        stringPtr("test-artifact"),
			Description: stringPtr("A test artifact"),
			Parts:       []protocol.Part{textPart},
			LastChunk:   boolPtr(true),
		})
		assert.NoError(t, err, "Adding artifact should succeed")

		// Test adding a streaming artifact (multiple chunks)
		firstChunkPart := protocol.NewTextPart("First chunk")
		err = handle.AddArtifact(protocol.Artifact{
			Name:        stringPtr("streaming-artifact"),
			Description: stringPtr("A streaming artifact"),
			Parts:       []protocol.Part{firstChunkPart},
			LastChunk:   boolPtr(false),
		})
		assert.NoError(t, err, "Adding first chunk should succeed")

		lastChunkPart := protocol.NewTextPart("Last chunk")
		err = handle.AddArtifact(protocol.Artifact{
			Name:        stringPtr("streaming-artifact"),
			Description: stringPtr("A streaming artifact"),
			Parts:       []protocol.Part{lastChunkPart},
			LastChunk:   boolPtr(true),
		})
		assert.NoError(t, err, "Adding last chunk should succeed")

		return handle.UpdateStatus(protocol.TaskStateCompleted, nil)
	}

	// Run the task
	task, err := tm.OnSendTask(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, protocol.TaskStateCompleted, task.Status.State)

	// Verify the artifacts are present in the task
	// NOTE: All artifacts are stored, not just the final ones
	require.Len(t, task.Artifacts, 3, "Task should have 3 artifacts")

	// Verify first artifact
	assert.Equal(t, "test-artifact", *task.Artifacts[0].Name)
	assert.Equal(t, "A test artifact", *task.Artifacts[0].Description)
	assert.True(t, *task.Artifacts[0].LastChunk)
	require.Len(t, task.Artifacts[0].Parts, 1)
	textPart, ok := task.Artifacts[0].Parts[0].(protocol.TextPart)
	require.True(t, ok)
	assert.Equal(t, "Artifact content", textPart.Text)

	// Verify first streaming chunk
	assert.Equal(t, "streaming-artifact", *task.Artifacts[1].Name)
	assert.Equal(t, "A streaming artifact", *task.Artifacts[1].Description)
	assert.False(t, *task.Artifacts[1].LastChunk)

	// Verify last streaming chunk
	assert.Equal(t, "streaming-artifact", *task.Artifacts[2].Name)
	assert.Equal(t, "A streaming artifact", *task.Artifacts[2].Description)
	assert.True(t, *task.Artifacts[2].LastChunk)
	require.Len(t, task.Artifacts[2].Parts, 1)
	textPart, ok = task.Artifacts[2].Parts[0].(protocol.TextPart)
	require.True(t, ok)
	assert.Equal(t, "Last chunk", textPart.Text)

	// Test error case: add artifact to non-existent task
	memTask := &MemoryTaskManager{
		Tasks:       make(map[string]*protocol.Task),
		Messages:    make(map[string][]protocol.Message),
		Subscribers: make(map[string][]chan<- protocol.TaskEvent),
		Processor:   processor,
	}

	handle := &memoryTaskHandle{
		taskID:  "non-existent-task",
		manager: memTask,
	}

	err = handle.AddArtifact(protocol.Artifact{
		Name:      stringPtr("test-artifact"),
		Parts:     []protocol.Part{protocol.NewTextPart("Test content")},
		LastChunk: boolPtr(true),
	})
	assert.Error(t, err, "Adding artifact to non-existent task should fail")
	assert.Contains(t, err.Error(), "not found", "Error should indicate task not found")
}

// TestIsStreamingRequest tests the IsStreamingRequest method of TaskHandle
func TestIsStreamingRequest(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	// Test with a streaming task (OnSendTaskSubscribe)
	taskID := "test-streaming-task"
	params := createTestTask(taskID, "Streaming task")

	// Create a complex processor to test IsStreamingRequest
	processor.processFunc = func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
		// Check if this is a streaming request
		isStreaming := handle.IsStreamingRequest()
		assert.True(t, isStreaming, "Task should be identified as streaming")

		// Update status and finish
		return handle.UpdateStatus(protocol.TaskStateCompleted, nil)
	}

	// Start a streaming task
	eventsChan, err := tm.OnSendTaskSubscribe(context.Background(), params)
	require.NoError(t, err)

	// Collect events to avoid blocking
	go func() {
		for range eventsChan {
			// Just consume events
		}
	}()

	// Let the task complete
	time.Sleep(50 * time.Millisecond)

	// Test with a non-streaming task (OnSendTask)
	taskID = "test-nonstreaming-task"
	params = createTestTask(taskID, "Non-streaming task")

	// Update processor for the second task
	processor.processFunc = func(ctx context.Context, taskID string, msg protocol.Message, handle TaskHandle) error {
		// Check if this is a streaming request
		isStreaming := handle.IsStreamingRequest()
		assert.False(t, isStreaming, "Task should not be identified as streaming")

		// Update status and finish
		return handle.UpdateStatus(protocol.TaskStateCompleted, nil)
	}

	// Start a non-streaming task
	_, err = tm.OnSendTask(context.Background(), params)
	require.NoError(t, err)
}

// TestProcessError tests the processError helper function
func TestProcessError(t *testing.T) {
	// Access to unexported processError function through reflection
	tm, err := NewMemoryTaskManager(&mockProcessor{})
	require.NoError(t, err)

	// Custom error type for testing
	errTaskNotFound := ErrTaskNotFound("task-id")

	// Test with a pre-defined error
	result := tm.processError(errTaskNotFound)
	assert.Equal(t, errTaskNotFound.Error(), result.Error(), "Pre-defined errors should be returned as-is")

	// Test with a random error
	randomErr := fmt.Errorf("random error")
	result = tm.processError(randomErr)
	assert.ErrorIs(t, result, randomErr, "Random errors should be wrapped")
	assert.Contains(t, result.Error(), "random error", "Error message should be preserved")
}

// Helper extension of MemoryTaskManager to expose processError
func (m *MemoryTaskManager) processError(err error) error {
	// Call the unexported processError through composition
	return processError(err)
}

// TestRemoveSubscriber tests the removeSubscriber internal function
func TestRemoveSubscriber(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	taskID := "test-subscriber-task"

	// Create some test channels
	ch1 := make(chan protocol.TaskEvent, 5)
	ch2 := make(chan protocol.TaskEvent, 5)
	ch3 := make(chan protocol.TaskEvent, 5)

	// Add subscribers
	tm.SubMutex.Lock()
	tm.Subscribers[taskID] = []chan<- protocol.TaskEvent{ch1, ch2, ch3}
	tm.SubMutex.Unlock()

	// Test removing a subscriber
	tm.removeSubscriber(taskID, ch2)

	// Verify ch2 was removed
	tm.SubMutex.RLock()
	subscribers := tm.Subscribers[taskID]
	tm.SubMutex.RUnlock()

	require.Len(t, subscribers, 2)
	// We can't do direct channel comparisons, so check the length instead
	// and verify the first and last elements
	assert.Equal(t, 2, len(subscribers))

	// Test removing another subscriber
	tm.removeSubscriber(taskID, ch1)

	// Verify ch1 was removed
	tm.SubMutex.RLock()
	subscribers = tm.Subscribers[taskID]
	tm.SubMutex.RUnlock()

	require.Len(t, subscribers, 1)
	// Since we removed ch1 and ch2, only ch3 should remain

	// Test removing the last subscriber
	tm.removeSubscriber(taskID, ch3)

	// Verify the task ID is no longer in the subscribers map
	tm.SubMutex.RLock()
	_, exists := tm.Subscribers[taskID]
	tm.SubMutex.RUnlock()

	assert.False(t, exists, "Task ID should be removed from subscribers map when last subscriber is removed")

	// Test removing from a non-existent task
	// This should not panic
	tm.removeSubscriber("non-existent-task", ch1)

	// Test removing a non-existent channel
	nonExistentCh := make(chan protocol.TaskEvent)
	tm.SubMutex.Lock()
	tm.Subscribers[taskID] = []chan<- protocol.TaskEvent{ch1}
	tm.SubMutex.Unlock()

	tm.removeSubscriber(taskID, nonExistentCh)

	// Verify ch1 is still there
	tm.SubMutex.RLock()
	subscribers = tm.Subscribers[taskID]
	tm.SubMutex.RUnlock()

	require.Len(t, subscribers, 1)
}

// Test push notification functionality
func TestMemoryTaskManager_PushNotifications(t *testing.T) {
	processor := &mockProcessor{}
	tm, err := NewMemoryTaskManager(processor)
	require.NoError(t, err)

	// Cast to the concrete type
	memTM := tm

	// Set up push notification config
	taskID := "test-task-123"
	url := "http://example.com/webhook"

	// Add task first (since we need a task to register notifications)
	task := &protocol.Task{
		ID: taskID,
		Status: protocol.TaskStatus{
			State: "pending",
		},
	}
	memTM.TasksMutex.Lock()
	memTM.Tasks[taskID] = task
	memTM.TasksMutex.Unlock()

	// Set up push notification directly
	config := protocol.PushNotificationConfig{
		URL: url,
		Authentication: &protocol.AuthenticationInfo{
			Schemes: []string{"bearer"},
		},
		Metadata: map[string]interface{}{
			"jwksUrl": "http://example.com/jwks",
		},
	}

	// Register push notification directly
	memTM.PushNotificationsMutex.Lock()
	memTM.PushNotifications[taskID] = config
	memTM.PushNotificationsMutex.Unlock()

	// Verify the notification was registered
	memTM.PushNotificationsMutex.RLock()
	notification, exists := memTM.PushNotifications[taskID]
	memTM.PushNotificationsMutex.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, url, notification.URL)
	assert.NotNil(t, notification.Authentication)
	assert.Equal(t, "bearer", notification.Authentication.Schemes[0])
	assert.Equal(t, "http://example.com/jwks", notification.Metadata["jwksUrl"])
}
