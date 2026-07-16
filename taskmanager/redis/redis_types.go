// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides Redis-specific implementations of taskmanager interfaces.
package redis

import (
	"fmt"
	"sync"
	"sync/atomic"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// taskSubscriber is an in-process event channel attached to a task. It is
// internal machinery: the MessageProcessor contract exposes only event channels, so
// subscribers are created by the manager for resubscribe and for the
// message/stream request pipe. Cross-node resubscribe tailers also feed one of
// these local channels; the subscriber itself has no distributed semantics.
type taskSubscriber struct {
	taskID     string
	eventQueue chan protocol.StreamResponse
	// done is closed before the event channel so a blocking Send unblocks the
	// instant Close runs, even while the write lock is held.
	done   chan struct{}
	closed atomic.Bool
	// mu serializes Send against Close so an event is never sent on a closed channel.
	mu sync.RWMutex
	// blockingSend selects backpressure over drop: a full buffer blocks the
	// sender instead of returning an error.
	blockingSend bool
}

// newTaskSubscriber creates a subscriber with the given buffer size (the
// manager default applies when size <= 0).
func newTaskSubscriber(taskID string, bufferSize int, blockingSend bool) *taskSubscriber {
	if bufferSize <= 0 {
		bufferSize = defaultTaskSubscriberBufferSize
	}
	return &taskSubscriber{
		taskID:       taskID,
		eventQueue:   make(chan protocol.StreamResponse, bufferSize),
		done:         make(chan struct{}),
		blockingSend: blockingSend,
	}
}

// Send delivers an event to the subscriber's channel. With blockingSend the
// call waits for buffer space; otherwise a full buffer is an error and the
// event is dropped for this subscriber (storage already has it). A concurrent
// Close always releases a blocked Send through done.
func (s *taskSubscriber) Send(event protocol.StreamResponse) error {
	if s.Closed() {
		return fmt.Errorf("task subscriber for task %s is closed", s.taskID)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Closed() {
		return fmt.Errorf("task subscriber for task %s is closed", s.taskID)
	}

	if s.blockingSend {
		select {
		case s.eventQueue <- event:
			return nil
		case <-s.done:
			return fmt.Errorf("task subscriber for task %s is closed", s.taskID)
		}
	}

	select {
	case s.eventQueue <- event:
		return nil
	case <-s.done:
		return fmt.Errorf("task subscriber for task %s is closed", s.taskID)
	default:
		return fmt.Errorf("event queue is full for task %s", s.taskID)
	}
}

// Channel returns the receive side handed to the client.
func (s *taskSubscriber) Channel() <-chan protocol.StreamResponse {
	return s.eventQueue
}

// Closed returns true if the subscriber is closed.
func (s *taskSubscriber) Closed() bool {
	return s.closed.Load()
}

// Close closes the subscriber and its event channel. It is safe to call
// multiple times and unblocks any in-flight blocking Send (done is closed
// before the write lock is taken).
func (s *taskSubscriber) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	close(s.done)

	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.eventQueue)
}
