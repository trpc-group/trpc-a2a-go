// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides configuration options for RedisTaskManager.
package redis

import (
	"time"
)

// TaskManagerOptions contains configuration options for RedisTaskManager.
type TaskManagerOptions struct {
	// ExpireTime is the time after which Redis keys expire.
	ExpireTime time.Duration

	// MaxHistoryLength is the maximum number of messages to keep in conversation history.
	MaxHistoryLength int

	// TaskSubscriberBufSize is the buffer size for task subscriber channels.
	TaskSubscriberBufSize int

	// TaskSubscriberBlockingSend enables blocking send for task subscribers.
	TaskSubscriberBlockingSend bool
}

// DefaultRedisTaskManagerOptions returns the default configuration options.
func DefaultRedisTaskManagerOptions() *TaskManagerOptions {
	return &TaskManagerOptions{
		ExpireTime:                 defaultExpiration,
		MaxHistoryLength:           defaultMaxHistoryLength,
		TaskSubscriberBufSize:      defaultTaskSubscriberBufferSize,
		TaskSubscriberBlockingSend: false,
	}
}

// TaskManagerOption defines a function type for configuring RedisTaskManager.
type TaskManagerOption func(*TaskManagerOptions)

// WithExpireTime sets the expiration time for Redis keys.
func WithExpireTime(expireTime time.Duration) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		if expireTime > 0 {
			opts.ExpireTime = expireTime
		}
	}
}

// WithMaxHistoryLength sets the maximum number of messages to keep in conversation history.
func WithMaxHistoryLength(length int) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		if length > 0 {
			opts.MaxHistoryLength = length
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
