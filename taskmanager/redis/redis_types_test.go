// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestTaskSubscriber(t *testing.T) {
	taskID := "test-task-2"
	bufferSize := 5

	// Create subscriber
	subscriber := newTaskSubscriber(taskID, bufferSize, false)

	if subscriber.Closed() {
		t.Error("Subscriber should not be closed initially")
	}

	// Test sending events
	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateSubmitted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	})

	err := subscriber.Send(event)
	if err != nil {
		t.Errorf("Unexpected error sending event: %v", err)
	}

	// Test receiving events
	select {
	case receivedEvent := <-subscriber.Channel():
		if receivedEvent.GetStatusUpdate() == nil {
			t.Error("Expected status update, got nil")
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

func TestTaskSubscriberBufferFull(t *testing.T) {
	taskID := "test-task-3"
	bufferSize := 2

	subscriber := newTaskSubscriber(taskID, bufferSize, false)
	defer subscriber.Close()

	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
	})

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

func TestTaskSubscriberBlockingSend(t *testing.T) {
	subscriber := newTaskSubscriber("test-task-4", 1, true)
	defer subscriber.Close()

	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: "test-task-4",
	})

	if err := subscriber.Send(event); err != nil {
		t.Fatalf("Unexpected error sending event: %v", err)
	}

	// A blocked Send must complete once the consumer drains the buffer.
	sent := make(chan error, 1)
	go func() {
		sent <- subscriber.Send(event)
	}()

	select {
	case err := <-sent:
		t.Fatalf("Send returned before buffer had room: %v", err)
	case <-time.After(20 * time.Millisecond):
		// Still blocked, as expected.
	}

	<-subscriber.Channel()
	select {
	case err := <-sent:
		if err != nil {
			t.Errorf("Unexpected error from blocking send: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("blocking send did not complete after buffer drained")
	}
}
