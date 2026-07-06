// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import "testing"

func TestTaskSubscriber_DefaultBuffer(t *testing.T) {
	sub := newTaskSubscriber("task-x", 0, false)
	defer sub.Close()
	if cap(sub.eventQueue) != defaultTaskSubscriberBufferSize {
		t.Fatalf("unexpected default buffer size, got %d, want %d",
			cap(sub.eventQueue), defaultTaskSubscriberBufferSize)
	}
}

func TestTaskSubscriber_CustomBuffer(t *testing.T) {
	const want = 11
	sub := newTaskSubscriber("task-y", want, false)
	defer sub.Close()
	if cap(sub.eventQueue) != want {
		t.Fatalf("unexpected buffer size, got %d, want %d",
			cap(sub.eventQueue), want)
	}
}
