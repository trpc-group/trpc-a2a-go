// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager provides configuration options for MemoryTaskManager.
package taskmanager

import (
	"time"
)

// MemoryTaskManagerOptions contains configuration options for MemoryTaskManager.
type MemoryTaskManagerOptions struct {
	// MaxHistoryLength is the maximum number of messages to keep in conversation history.
	MaxHistoryLength int

	// ConversationTTL is the maximum lifetime of conversations.
	ConversationTTL time.Duration

	// CleanupInterval is the interval for cleanup checks.
	CleanupInterval time.Duration

	// EnableCleanup enables automatic cleanup of expired conversations.
	EnableCleanup bool

	// TaskSubscriberBufSize is the buffer size for task subscribers.
	TaskSubscriberBufSize int

	// TaskSubscriberBlockingSend enables blocking send for task subscribers.
	TaskSubscriberBlockingSend bool
}

// DefaultMemoryTaskManagerOptions returns the default configuration options.
func DefaultMemoryTaskManagerOptions() *MemoryTaskManagerOptions {
	return &MemoryTaskManagerOptions{
		MaxHistoryLength:           defaultMaxHistoryLength,
		ConversationTTL:            defaultConversationTTL,
		CleanupInterval:            defaultCleanupInterval,
		EnableCleanup:              true,
		TaskSubscriberBufSize:      defaultSubscriberBufferSize,
		TaskSubscriberBlockingSend: false,
	}
}

// MemoryTaskManagerOption defines a function type for configuring MemoryTaskManager.
type MemoryTaskManagerOption func(*MemoryTaskManagerOptions)

// WithMaxHistoryLength sets the maximum number of messages to keep in conversation history.
func WithMaxHistoryLength(length int) MemoryTaskManagerOption {
	return func(opts *MemoryTaskManagerOptions) {
		if length > 0 {
			opts.MaxHistoryLength = length
		}
	}
}

// WithConversationTTL sets the conversation TTL, enabling automatic cleanup.
// ttl: the maximum lifetime of the conversation
// cleanupInterval: the interval time for cleanup check
func WithConversationTTL(ttl, cleanupInterval time.Duration) MemoryTaskManagerOption {
	return func(opts *MemoryTaskManagerOptions) {
		if ttl > 0 && cleanupInterval > 0 {
			opts.ConversationTTL = ttl
			opts.CleanupInterval = cleanupInterval
			opts.EnableCleanup = true
		}
	}
}

// WithTaskSubscriberBufferSize sets the buffer size for task subscriber channels.
func WithTaskSubscriberBufferSize(size int) MemoryTaskManagerOption {
	return func(opts *MemoryTaskManagerOptions) {
		if size > 0 {
			opts.TaskSubscriberBufSize = size
		}
	}
}

// WithTaskSubscriberBlockingSend sets the blocking send flag for the task subscriber
func WithTaskSubscriberBlockingSend(blockingSend bool) MemoryTaskManagerOption {
	return func(opts *MemoryTaskManagerOptions) {
		opts.TaskSubscriberBlockingSend = blockingSend
	}
}
