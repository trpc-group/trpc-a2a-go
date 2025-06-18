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

// RedisCancellableTask implements the CancellableTask interface for Redis storage.
type RedisCancellableTask struct {
	task       *protocol.Task
	cancelFunc context.CancelFunc
	mu         sync.RWMutex
}

// NewRedisCancellableTask creates a new Redis-based cancellable task.
func NewRedisCancellableTask(task *protocol.Task, cancelFunc context.CancelFunc) *RedisCancellableTask {
	return &RedisCancellableTask{
		task:       task,
		cancelFunc: cancelFunc,
	}
}

// Task returns the protocol task.
func (t *RedisCancellableTask) Task() *protocol.Task {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.task
}

// Cancel cancels the task by calling the cancel function.
func (t *RedisCancellableTask) Cancel() {
	if t.cancelFunc != nil {
		t.cancelFunc()
	}
}

// RedisTaskSubscriber implements the TaskSubscriber interface for Redis storage.
type RedisTaskSubscriber struct {
	taskID     string
	eventQueue chan protocol.StreamingMessageEvent
	closed     atomic.Bool
	mu         sync.RWMutex
	lastAccess time.Time
}

// NewRedisTaskSubscriber creates a new Redis-based task subscriber.
func NewRedisTaskSubscriber(taskID string, bufferSize int) *RedisTaskSubscriber {
	if bufferSize <= 0 {
		bufferSize = defaultTaskSubscriberBufferSize
	}

	return &RedisTaskSubscriber{
		taskID:     taskID,
		eventQueue: make(chan protocol.StreamingMessageEvent, bufferSize),
		lastAccess: time.Now(),
	}
}

// Send sends an event to the subscriber's event queue.
func (s *RedisTaskSubscriber) Send(event protocol.StreamingMessageEvent) error {
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
func (s *RedisTaskSubscriber) Channel() <-chan protocol.StreamingMessageEvent {
	return s.eventQueue
}

// Closed returns true if the subscriber is closed.
func (s *RedisTaskSubscriber) Closed() bool {
	return s.closed.Load()
}

// Close closes the subscriber and its event channel.
func (s *RedisTaskSubscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed.Load() {
		s.closed.Store(true)
		close(s.eventQueue)
	}
}

// GetTaskID returns the task ID this subscriber is associated with.
func (s *RedisTaskSubscriber) GetTaskID() string {
	return s.taskID
}

// GetLastAccessTime returns the last access time of the subscriber.
func (s *RedisTaskSubscriber) GetLastAccessTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccess
}

// Ensure our types implement the required interfaces
var _ taskmanager.CancellableTask = (*RedisCancellableTask)(nil)
var _ taskmanager.TaskSubscriber = (*RedisTaskSubscriber)(nil)
