package redis

import "testing"

func TestTaskSubscriber_DefaultBuffer(t *testing.T) {
	sub := NewTaskSubscriber("task-x", 0)
	defer sub.Close()
	if cap(sub.eventQueue) != defaultTaskSubscriberBufferSize {
		t.Fatalf("unexpected default buffer size, got %d, want %d",
			cap(sub.eventQueue), defaultTaskSubscriberBufferSize)
	}
}

func TestTaskSubscriber_CustomBuffer(t *testing.T) {
	const want = 11
	sub := NewTaskSubscriber("task-y", want)
	defer sub.Close()
	if cap(sub.eventQueue) != want {
		t.Fatalf("unexpected buffer size, got %d, want %d",
			cap(sub.eventQueue), want)
	}
}
