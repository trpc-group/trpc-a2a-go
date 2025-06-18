// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides Redis-specific implementations of taskmanager interfaces.
package redis

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// CancellableTask implements the CancellableTask interface for Redis storage.
type CancellableTask struct {
	task       *protocol.Task
	cancelFunc context.CancelFunc
	mu         sync.RWMutex
}

// NewRedisCancellableTask creates a new Redis-based cancellable task.
func NewRedisCancellableTask(task *protocol.Task, cancelFunc context.CancelFunc) *CancellableTask {
	return &CancellableTask{
		task:       task,
		cancelFunc: cancelFunc,
	}
}

// Task returns the protocol task.
func (t *CancellableTask) Task() *protocol.Task {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.task
}

// Cancel cancels the task by calling the cancel function.
func (t *CancellableTask) Cancel() {
	if t.cancelFunc != nil {
		t.cancelFunc()
	}
}

// TaskSubscriber implements the TaskSubscriber interface for Redis storage.
type TaskSubscriber struct {
	taskID     string
	eventQueue chan protocol.StreamingMessageEvent
	closed     atomic.Bool
	mu         sync.RWMutex
	lastAccess time.Time
}

// NewTaskSubscriber creates a new Redis-based task subscriber.
func NewTaskSubscriber(taskID string, bufferSize int) *TaskSubscriber {
	if bufferSize <= 0 {
		bufferSize = defaultTaskSubscriberBufferSize
	}

	return &TaskSubscriber{
		taskID:     taskID,
		eventQueue: make(chan protocol.StreamingMessageEvent, bufferSize),
		lastAccess: time.Now(),
	}
}

// Send sends an event to the subscriber's event queue.
func (s *TaskSubscriber) Send(event protocol.StreamingMessageEvent) error {
	if s.Closed() {
		return fmt.Errorf("task subscriber for task %s is closed", s.taskID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Closed() {
		return fmt.Errorf("task subscriber for task %s is closed", s.taskID)
	}

	s.lastAccess = time.Now()

	// Use select with default to avoid blocking
	select {
	case s.eventQueue <- event:
		return nil
	default:
		return fmt.Errorf("event queue is full for task %s", s.taskID)
	}
}

// Channel returns the event channel for receiving streaming events.
func (s *TaskSubscriber) Channel() <-chan protocol.StreamingMessageEvent {
	return s.eventQueue
}

// Closed returns true if the subscriber is closed.
func (s *TaskSubscriber) Closed() bool {
	return s.closed.Load()
}

// Close closes the subscriber and its event channel.
func (s *TaskSubscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed.Load() {
		s.closed.Store(true)
		close(s.eventQueue)
	}
}

// GetTaskID returns the task ID this subscriber is associated with.
func (s *TaskSubscriber) GetTaskID() string {
	return s.taskID
}

// GetLastAccessTime returns the last access time of the subscriber.
func (s *TaskSubscriber) GetLastAccessTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccess
}

// Ensure our types implement the required interfaces
var _ taskmanager.CancellableTask = (*CancellableTask)(nil)
var _ taskmanager.TaskSubscriber = (*TaskSubscriber)(nil)
