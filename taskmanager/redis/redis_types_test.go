// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	v1 "trpc.group/trpc-go/trpc-a2a-go/protocol/a2apb"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

func TestRedisCancellableTask(t *testing.T) {
	// Create a test task
	task := &protocol.Task{
		Task: &v1.Task{
			Id: "test-task-1",
			Status: &v1.TaskStatus{
				State:     protocol.TaskStateSubmitted,
				Timestamp: timestamppb.New(time.Now().UTC()),
			},
		},
	}

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Create our Redis cancellable task
	cancellableTask := NewRedisCancellableTask(task, cancel)

	// Verify it implements the interface
	var _ taskmanager.CancellableTask = cancellableTask

	// Test Task() method
	retrievedTask := cancellableTask.Task()
	if retrievedTask.Id != "test-task-1" {
		t.Errorf("Expected task ID 'test-task-1', got '%s'", retrievedTask.Id)
	}

	// Test Cancel() method
	cancellableTask.Cancel()

	// Verify context was cancelled
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected context to be cancelled")
	}
}

func TestRedisTaskSubscriber(t *testing.T) {
	taskID := "test-task-2"
	bufferSize := 5

	// Create subscriber
	subscriber := NewTaskSubscriber(taskID, bufferSize)

	// Verify it implements the interface
	var _ taskmanager.TaskSubscriber = subscriber

	// Test basic properties
	if subscriber.GetTaskID() != taskID {
		t.Errorf("Expected task ID '%s', got '%s'", taskID, subscriber.GetTaskID())
	}

	if subscriber.Closed() {
		t.Error("Subscriber should not be closed initially")
	}

	// Test sending events
	statusUpdateEvent := protocol.NewTaskStatusUpdateEvent(
		taskID,
		"",
		&v1.TaskStatus{
			State:     protocol.TaskStateSubmitted,
			Timestamp: timestamppb.New(time.Now().UTC()),
		},
		false,
	)
	event := protocol.StreamingMessageEvent{
		Result: &statusUpdateEvent,
	}

	err := subscriber.Send(event)
	if err != nil {
		t.Errorf("Unexpected error sending event: %v", err)
	}

	// Test receiving events
	select {
	case receivedEvent := <-subscriber.Channel():
		if receivedEvent.Result == nil {
			t.Error("Expected event result, got nil")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for event")
	}

	// Test closing
	subscriber.Close()
	if !subscriber.Closed() {
		t.Error("Subscriber should be closed after Close()")
	}

	// Test sending to closed subscriber
	err = subscriber.Send(event)
	if err == nil {
		t.Error("Expected error when sending to closed subscriber")
	}
}

func TestRedisTaskSubscriberBufferFull(t *testing.T) {
	taskID := "test-task-3"
	bufferSize := 2

	subscriber := NewTaskSubscriber(taskID, bufferSize)
	defer subscriber.Close()

	statusUpdateEvent := protocol.NewTaskStatusUpdateEvent(taskID, "", &v1.TaskStatus{State: protocol.TaskStateSubmitted}, false)
	event := protocol.StreamingMessageEvent{
		Result: &statusUpdateEvent,
	}

	// Fill the buffer
	for i := 0; i < bufferSize; i++ {
		err := subscriber.Send(event)
		if err != nil {
			t.Errorf("Unexpected error sending event %d: %v", i, err)
		}
	}

	// Next send should fail due to full buffer
	err := subscriber.Send(event)
	if err == nil {
		t.Error("Expected error when buffer is full")
	}
}
