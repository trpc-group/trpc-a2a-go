// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.
package redis

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// testProcessor is a test implementation of the TaskProcessor interface
// It simulates specific behaviors based on the input message.
type testProcessor struct {
	waitTime time.Duration // Used to control how long the processor takes
	mu       sync.Mutex
	tasks    map[string]bool
}

func newTestProcessor() *testProcessor {
	return &testProcessor{
		waitTime: 50 * time.Millisecond,
		tasks:    make(map[string]bool),
	}
}

// Process implements TaskProcessor
func (p *testProcessor) Process(
	ctx context.Context,
	taskID string,
	initialMsg protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	p.mu.Lock()
	p.tasks[taskID] = true
	p.mu.Unlock()

	// Extract command from the message text
	var command string
	var messageText string
	if len(initialMsg.Parts) > 0 {
		if textPart, ok := initialMsg.Parts[0].(protocol.TextPart); ok {
			messageText = textPart.Text
			// Extract command if format is "command:message"
			for _, prefix := range []string{"sleep:", "fail:", "cancel:", "artifacts:", "input-required:"} {
				if len(messageText) > len(prefix) && messageText[:len(prefix)] == prefix {
					command = prefix[:len(prefix)-1]
					messageText = messageText[len(prefix):]
					break
				}
			}
		}
	}

	// Simulate slow processing if requested
	if command == "sleep" {
		sleepDuration := p.waitTime
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDuration):
			// Continue processing
		}
	}

	// Simulate a failure if requested
	if command == "fail" {
		handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
			Role:  protocol.MessageRoleAgent,
			Parts: []protocol.Part{protocol.NewTextPart("Processing started but will fail")},
		})
		return fmt.Errorf("task failed as requested: %s", messageText)
	}

	// Check for cancellation request during processing
	if command == "cancel" {
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				// Update status to show we're still working
				handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
					Role:  protocol.MessageRoleAgent,
					Parts: []protocol.Part{protocol.NewTextPart(fmt.Sprintf("Still working... %d", i))},
				})
			}
		}
	}

	// Simulate returning multiple artifacts if requested
	if command == "artifacts" {
		for i := 0; i < 3; i++ {
			artifact := protocol.Artifact{
				Name: stringPtr(fmt.Sprintf("artifact-%d", i)),
				Parts: []protocol.Part{
					protocol.NewTextPart(fmt.Sprintf("Artifact content %d: %s", i, messageText)),
				},
				Index: i,
			}
			isLast := i == 2
			artifact.LastChunk = &isLast
			if err := handle.AddArtifact(artifact); err != nil {
				return err
			}
			time.Sleep(20 * time.Millisecond) // Small delay between artifacts
		}
	}

	// Simulate requiring input if requested
	if command == "input-required" {
		return handle.UpdateStatus(protocol.TaskStateInputRequired, &protocol.Message{
			Role:  protocol.MessageRoleAgent,
			Parts: []protocol.Part{protocol.NewTextPart("Please provide more input: " + messageText)},
		})
	}

	// Default processing for normal tasks
	return handle.UpdateStatus(protocol.TaskStateCompleted, &protocol.Message{
		Role:  protocol.MessageRoleAgent,
		Parts: []protocol.Part{protocol.NewTextPart("Task completed successfully: " + messageText)},
	})
}

func stringPtr(s string) *string {
	return &s
}

// setupRedisTest creates an in-memory Redis server and returns a configured TaskManager
func setupRedisTest(t *testing.T) (*TaskManager, *miniredis.Miniredis) {
	// Create a new in-memory Redis server
	mr, err := miniredis.Run()
	require.NoError(t, err, "Failed to create miniredis server")

	// Create Redis client options pointing to miniredis
	opts := &redis.UniversalOptions{
		Addrs: []string{mr.Addr()},
	}

	// Create a test processor
	processor := newTestProcessor()

	// Create the Redis task manager
	expiration := 1 * time.Hour
	client := redis.NewUniversalClient(opts)
	manager, err := NewRedisTaskManager(client, processor, WithExpiration(expiration))
	require.NoError(t, err, "Failed to create Redis task manager")

	return manager, mr
}

// Test basic task processing with direct task submission
func TestE2E_BasicTaskProcessing(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a task
	taskParams := protocol.SendTaskParams{
		ID: "test-task-1",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("Hello, this is a test task")},
		},
	}

	// Submit the task (simulates client -> server -> task manager flow)
	task, err := manager.OnSendTask(ctx, taskParams)
	require.NoError(t, err, "Failed to send task")
	assert.Equal(t, protocol.TaskStateCompleted, task.Status.State, "Task should be completed")

	// Verify the task was stored in Redis
	taskKey := "task:" + taskParams.ID
	assert.True(t, mr.Exists(taskKey), "Task should be stored in Redis")

	// Verify message history was stored in Redis
	messageKey := "msg:" + taskParams.ID
	assert.True(t, mr.Exists(messageKey), "Message history should be stored in Redis")

	// Retrieve the task to verify content
	retrievedTask, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{
		ID:            taskParams.ID,
		HistoryLength: intPtr(10),
	})
	require.NoError(t, err, "Failed to retrieve task")

	// Verify the final state and response message
	assert.Equal(t, protocol.TaskStateCompleted, retrievedTask.Status.State)
	require.NotNil(t, retrievedTask.Status.Message, "Response message should not be nil")
	assert.Equal(t, protocol.MessageRoleAgent, retrievedTask.Status.Message.Role)
	require.Greater(t, len(retrievedTask.Status.Message.Parts), 0, "Response should have message parts")

	// Verify message history
	assert.Greater(t, len(retrievedTask.History), 0, "Task should have message history")
}

// Test task processing with streaming/subscription
func TestE2E_TaskSubscription(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a task with artifact generation
	taskParams := protocol.SendTaskParams{
		ID: "test-subscribe-task",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("artifacts:test artifacts")},
		},
	}

	// Subscribe to task events (simulates client -> server streaming connection)
	eventChan, err := manager.OnSendTaskSubscribe(ctx, taskParams)
	require.NoError(t, err, "Failed to subscribe to task")

	// Collect events
	var statusEvents []protocol.TaskStatusUpdateEvent
	var artifactEvents []protocol.TaskArtifactUpdateEvent
	var lastEvent protocol.TaskEvent

	// Process events until the channel is closed or timeout occurs
	timeout := time.After(3 * time.Second)
eventLoop:
	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				// Channel closed, processing complete
				break eventLoop
			}
			lastEvent = event

			// Collect events by type for verification
			switch e := event.(type) {
			case protocol.TaskStatusUpdateEvent:
				statusEvents = append(statusEvents, e)
			case protocol.TaskArtifactUpdateEvent:
				artifactEvents = append(artifactEvents, e)
			}
		case <-timeout:
			t.Fatal("Test timed out waiting for events")
			break eventLoop
		}
	}

	// Verify we received status updates
	assert.GreaterOrEqual(t, len(statusEvents), 2, "Should receive at least initial and final status updates")
	assert.Equal(t, taskParams.ID, statusEvents[0].ID, "Status event should have correct task ID")

	// Verify we received artifact events (the test processor sends 3)
	assert.Equal(t, 3, len(artifactEvents), "Should receive 3 artifact events")

	// Verify the last event was final
	assert.True(t, lastEvent.IsFinal(), "Last event should be final")

	// Retrieve the task to verify final state
	retrievedTask, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{
		ID: taskParams.ID,
	})
	require.NoError(t, err, "Failed to retrieve task")
	assert.Equal(t, protocol.TaskStateCompleted, retrievedTask.Status.State)
	assert.Equal(t, 3, len(retrievedTask.Artifacts), "Task should have 3 artifacts")
}

// Test task cancellation
func TestE2E_TaskCancellation(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a long-running task that can be cancelled
	taskParams := protocol.SendTaskParams{
		ID: "test-cancel-task",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("cancel:task to be cancelled")},
		},
	}

	// Start task with subscription
	eventChan, err := manager.OnSendTaskSubscribe(ctx, taskParams)
	require.NoError(t, err, "Failed to subscribe to task")

	// Make sure task has started (wait for working state event)
	var taskStarted bool
	timeout := time.After(2 * time.Second)
	for !taskStarted {
		select {
		case event, ok := <-eventChan:
			if !ok {
				t.Fatal("Event channel closed before task started")
			}
			if statusEvent, ok := event.(protocol.TaskStatusUpdateEvent); ok {
				if statusEvent.Status.State == protocol.TaskStateWorking {
					taskStarted = true
				}
			}
		case <-timeout:
			t.Fatal("Timed out waiting for task to start")
		}
	}

	// Cancel the task once it's started
	cancelledTask, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{ID: taskParams.ID})
	require.NoError(t, err, "Failed to cancel task")
	assert.Equal(t, protocol.TaskStateCanceled, cancelledTask.Status.State, "Task should be in cancelled state")

	// Wait for final event or timeout
	var finalEventReceived bool
	timeout = time.After(2 * time.Second)
	for !finalEventReceived {
		select {
		case event, ok := <-eventChan:
			if !ok {
				// Channel closed normally
				finalEventReceived = true
				break
			}
			if event.IsFinal() {
				finalEventReceived = true
			}
		case <-timeout:
			t.Fatal("Timed out waiting for final event after cancellation")
		}
	}

	// Verify the final state
	retrievedTask, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: taskParams.ID})
	require.NoError(t, err, "Failed to retrieve task")
	assert.Equal(t, protocol.TaskStateCanceled, retrievedTask.Status.State, "Task should remain in cancelled state")
}

// Test task resubscription
func TestE2E_TaskResubscribe(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create and complete a task first
	taskParams := protocol.SendTaskParams{
		ID: "test-resubscribe-task",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("Hello, this is a test task")},
		},
	}

	// Complete the task
	task, err := manager.OnSendTask(ctx, taskParams)
	require.NoError(t, err, "Failed to send task")
	assert.Equal(t, protocol.TaskStateCompleted, task.Status.State)

	// Now try to resubscribe to the completed task
	resubEventChan, err := manager.OnResubscribe(
		ctx, protocol.TaskIDParams{ID: taskParams.ID},
	)
	require.NoError(t, err, "Failed to resubscribe to task")

	// We should get a single final status event and then the channel should close
	event, ok := <-resubEventChan
	require.True(t, ok, "Should receive one event")
	assert.True(t, event.IsFinal(), "Event should be final")

	statusEvent, ok := event.(protocol.TaskStatusUpdateEvent)
	require.True(t, ok, "Event should be a status update")
	assert.Equal(t, protocol.TaskStateCompleted, statusEvent.Status.State)

	// Ensure channel closes
	_, ok = <-resubEventChan
	assert.False(t, ok, "Channel should be closed after final event")
}

// Test task push notification configuration
func TestE2E_PushNotifications(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a task
	taskParams := protocol.SendTaskParams{
		ID: "test-push-notification",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("Task with push notifications")},
		},
	}

	// Send the task
	_, err := manager.OnSendTask(ctx, taskParams)
	require.NoError(t, err, "Failed to send task")

	// Configure push notifications for the task
	pushConfig := protocol.TaskPushNotificationConfig{
		ID: taskParams.ID,
		PushNotificationConfig: protocol.PushNotificationConfig{
			URL:   "https://example.com/webhook",
			Token: "test-token",
		},
	}

	// Set push notification config
	resultConfig, err := manager.OnPushNotificationSet(ctx, pushConfig)
	require.NoError(t, err, "Failed to set push notification config")
	assert.Equal(t, pushConfig.ID, resultConfig.ID)
	assert.Equal(t, pushConfig.PushNotificationConfig.URL, resultConfig.PushNotificationConfig.URL)

	// Verify push notification key in Redis
	pushKey := "push:" + taskParams.ID
	assert.True(t, mr.Exists(pushKey), "Push notification config should be stored in Redis")

	// Get push notification config
	retrievedConfig, err := manager.OnPushNotificationGet(
		ctx, protocol.TaskIDParams{ID: taskParams.ID},
	)
	require.NoError(t, err, "Failed to get push notification config")
	assert.Equal(t, pushConfig.PushNotificationConfig.URL, retrievedConfig.PushNotificationConfig.URL)
	assert.Equal(t, pushConfig.PushNotificationConfig.Token, retrievedConfig.PushNotificationConfig.Token)
}

// Test error handling for non-existent tasks
func TestE2E_ErrorHandling(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nonExistentTaskID := "non-existent-task"

	// Try to get a non-existent task
	_, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: nonExistentTaskID})
	assert.Error(t, err, "Getting non-existent task should return an error")

	// Try to cancel a non-existent task
	_, err = manager.OnCancelTask(ctx, protocol.TaskIDParams{ID: nonExistentTaskID})
	assert.Error(t, err, "Cancelling non-existent task should return an error")

	// Try to get push notifications for a non-existent task
	_, err = manager.OnPushNotificationGet(ctx, protocol.TaskIDParams{ID: nonExistentTaskID})
	assert.Error(t, err, "Getting push notifications for non-existent task should return an error")

	// Try to set push notifications for a non-existent task
	_, err = manager.OnPushNotificationSet(ctx, protocol.TaskPushNotificationConfig{
		ID: nonExistentTaskID,
		PushNotificationConfig: protocol.PushNotificationConfig{
			URL: "https://example.com/webhook",
		},
	})
	assert.Error(t, err, "Setting push notifications for non-existent task should return an error")

	// Try to resubscribe to a non-existent task
	_, err = manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: nonExistentTaskID})
	assert.Error(t, err, "Resubscribing to non-existent task should return an error")
}

// Test handling of task failure
func TestE2E_TaskFailure(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a task that will fail
	taskParams := protocol.SendTaskParams{
		ID: "test-failure-task",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("fail:intentional failure")},
		},
	}

	// Send the task
	task, err := manager.OnSendTask(ctx, taskParams)
	// The task should succeed but the processor should report failure status
	require.NoError(t, err, "OnSendTask should not return an error even if task processing fails")
	assert.Equal(t, protocol.TaskStateFailed, task.Status.State, "Task should be in failed state")

	// Verify the failure was stored
	retrievedTask, err := manager.OnGetTask(
		ctx, protocol.TaskQueryParams{ID: taskParams.ID},
	)
	require.NoError(t, err, "Failed to retrieve task")
	assert.Equal(t, protocol.TaskStateFailed, retrievedTask.Status.State)
	require.NotNil(t, retrievedTask.Status.Message, "Failure message should be available")
}

// Test handling of input-required state
func TestE2E_InputRequired(t *testing.T) {
	manager, mr := setupRedisTest(t)
	defer mr.Close()
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a task that will require input
	taskParams := protocol.SendTaskParams{
		ID: "test-input-required",
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []protocol.Part{protocol.NewTextPart("input-required:more data needed")},
		},
	}

	// Send the task
	task, err := manager.OnSendTask(ctx, taskParams)
	require.NoError(t, err, "Failed to send task")
	assert.Equal(t, protocol.TaskStateInputRequired, task.Status.State, "Task should be in input-required state")

	// Verify the state was stored
	retrievedTask, err := manager.OnGetTask(
		ctx, protocol.TaskQueryParams{ID: taskParams.ID},
	)
	require.NoError(t, err, "Failed to retrieve task")
	assert.Equal(t, protocol.TaskStateInputRequired, retrievedTask.Status.State)
	require.NotNil(t, retrievedTask.Status.Message, "Input request message should be available")
}

func intPtr(i int) *int {
	return &i
}
