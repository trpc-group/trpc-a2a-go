// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import "time"

// TaskPersistenceMode selects how often the current Task snapshot is persisted.
type TaskPersistenceMode int

const (
	// TaskPersistencePerEvent writes the complete Task snapshot with every
	// Task-changing stream event. This preserves the historical behavior.
	TaskPersistencePerEvent TaskPersistenceMode = iota
	// TaskPersistenceCoalesced writes every stream event immediately but
	// coalesces complete Task snapshots by time and event-count thresholds.
	TaskPersistenceCoalesced
)

// TaskPersistenceConfig configures Task snapshot write coalescing.
//
// Redis Stream events are never coalesced: every event is still appended
// immediately for cross-node subscriptions. Only the complete task:* snapshot
// SET is delayed.
type TaskPersistenceConfig struct {
	Mode TaskPersistenceMode

	// SnapshotFlushInterval flushes a pending complete Task snapshot after the
	// stream becomes quiet for this duration. Zero disables time-based flushes.
	SnapshotFlushInterval time.Duration
	// MaxBufferedTaskEvents flushes after this many Task-changing artifact
	// events. Values <= 0 use the default of 32.
	MaxBufferedTaskEvents int
	// FlushOnGetTask flushes a locally owned pending snapshot before tasks/get.
	// A request handled by another replica may still observe a snapshot bounded
	// by the configured time/event thresholds.
	FlushOnGetTask bool
}

// DefaultTaskPersistenceConfig returns the historical per-event persistence mode.
func DefaultTaskPersistenceConfig() TaskPersistenceConfig {
	return TaskPersistenceConfig{
		Mode:                  TaskPersistencePerEvent,
		MaxBufferedTaskEvents: 32,
	}
}

// CoalescedTaskPersistenceDefaults returns recommended snapshot coalescing
// settings for high-volume artifact streams.
func CoalescedTaskPersistenceDefaults() TaskPersistenceConfig {
	return TaskPersistenceConfig{
		Mode:                  TaskPersistenceCoalesced,
		SnapshotFlushInterval: 200 * time.Millisecond,
		MaxBufferedTaskEvents: 32,
	}
}

func (c TaskPersistenceConfig) coalesced() bool {
	return c.Mode == TaskPersistenceCoalesced
}
