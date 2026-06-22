// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager provides configuration options for TaskManager.
package memory

import (
	"time"
)

// TaskManagerOptions contains configuration options for TaskManager.
type TaskManagerOptions struct {
	// MaxHistoryLength is the maximum number of messages to keep in conversation history.
	MaxHistoryLength int

	// ConversationTTL is the maximum lifetime of conversations.
	ConversationTTL time.Duration

	// TaskTTL is the maximum lifetime of tasks in terminal states (completed/failed/canceled/rejected).
	// Tasks that have been in a terminal state longer than this duration will be automatically cleaned up.
	// Default is 0 (disabled). Use WithTaskTTL to enable automatic task cleanup.
	TaskTTL time.Duration

	// CleanupInterval is the interval for cleanup checks.
	CleanupInterval time.Duration

	// EnableCleanup enables automatic cleanup of expired conversations and tasks.
	EnableCleanup bool

	// TaskSubscriberBufSize is the buffer size for task subscribers.
	TaskSubscriberBufSize int

	// TaskSubscriberBlockingSend enables blocking send for task subscribers.
	TaskSubscriberBlockingSend bool
}

// DefaultTaskManagerOptions returns the default configuration options.
func DefaultTaskManagerOptions() *TaskManagerOptions {
	return &TaskManagerOptions{
		MaxHistoryLength:           defaultMaxHistoryLength,
		ConversationTTL:            defaultConversationTTL,
		TaskTTL:                    defaultTaskTTL,
		CleanupInterval:            defaultCleanupInterval,
		EnableCleanup:              true,
		TaskSubscriberBufSize:      defaultSubscriberBufferSize,
		TaskSubscriberBlockingSend: false,
	}
}

// TaskManagerOption defines a function type for configuring TaskManager.
type TaskManagerOption func(*TaskManagerOptions)

// WithMaxHistoryLength sets the maximum number of messages to keep in conversation history.
func WithMaxHistoryLength(length int) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		if length > 0 {
			opts.MaxHistoryLength = length
		}
	}
}

// WithConversationTTL sets the conversation TTL, enabling automatic cleanup.
// ttl: the maximum lifetime of the conversation
// cleanupInterval: the interval time for cleanup check
func WithConversationTTL(ttl, cleanupInterval time.Duration) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		if ttl > 0 && cleanupInterval > 0 {
			opts.ConversationTTL = ttl
			opts.CleanupInterval = cleanupInterval
			opts.EnableCleanup = true
		}
	}
}

// WithTaskTTL sets the task TTL for automatic cleanup of tasks in terminal states.
// When enabled, tasks in terminal states (completed/failed/canceled/rejected) will be
// automatically removed after the specified duration. A TTL of 0 disables task cleanup.
// A positive ttl also enables the cleanup goroutine, mirroring WithConversationTTL.
func WithTaskTTL(ttl time.Duration) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		opts.TaskTTL = ttl
		if ttl > 0 {
			opts.EnableCleanup = true
		}
	}
}

// WithTaskSubscriberBufferSize sets the buffer size for task subscriber channels.
func WithTaskSubscriberBufferSize(size int) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		if size > 0 {
			opts.TaskSubscriberBufSize = size
		}
	}
}

// WithTaskSubscriberBlockingSend sets the blocking send flag for the task subscriber
func WithTaskSubscriberBlockingSend(blockingSend bool) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		opts.TaskSubscriberBlockingSend = blockingSend
	}
}
