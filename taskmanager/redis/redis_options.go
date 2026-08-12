// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides configuration options for RedisTaskManager.
package redis

import (
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

// TaskManagerOptions contains configuration options for RedisTaskManager.
type TaskManagerOptions struct {
	// ExpireTime is the time after which Redis keys expire.
	ExpireTime time.Duration

	// MaxHistoryLength is the maximum number of messages to keep in conversation history.
	MaxHistoryLength int

	// TaskSubscriberBufSize is the buffer size for task update events. A
	// SendStreamingMessage response pipe reserves one additional framing slot.
	TaskSubscriberBufSize int

	// TaskSubscriberBlockingSend enables blocking send for message/stream
	// response pipes. SubscribeToTask readers always use independent backpressure.
	TaskSubscriberBlockingSend bool

	// Push configures push-notification delivery (see push.Config). Push is
	// enabled by either a Sender for automatic delivery or ManualDelivery for
	// application-owned delivery.
	Push push.Config

	// CrossNodeResubscribe is retained for source compatibility. Redis
	// SubscribeToTask always uses Redis Streams and this value is ignored.
	// Deprecated: cross-node resubscribe is always enabled.
	CrossNodeResubscribe bool
}

// DefaultRedisTaskManagerOptions returns the default configuration options.
func DefaultRedisTaskManagerOptions() *TaskManagerOptions {
	return &TaskManagerOptions{
		ExpireTime:                 defaultExpiration,
		MaxHistoryLength:           defaultMaxHistoryLength,
		TaskSubscriberBufSize:      defaultTaskSubscriberBufferSize,
		TaskSubscriberBlockingSend: false,
		CrossNodeResubscribe:       true,
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

// WithTaskSubscriberBufferSize sets the buffer size for task update events. A
// SendStreamingMessage response pipe reserves one additional framing slot for
// its initial Task.
func WithTaskSubscriberBufferSize(size int) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		if size > 0 {
			opts.TaskSubscriberBufSize = size
		}
	}
}

// WithTaskSubscriberBlockingSend sets blocking send for message/stream response
// pipes. SubscribeToTask readers always block independently.
func WithTaskSubscriberBlockingSend(blockingSend bool) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		opts.TaskSubscriberBlockingSend = blockingSend
	}
}

// WithPushNotifications configures push notifications. A non-nil cfg.Sender
// enables automatic delivery to every webhook registered for the task. Set
// cfg.ManualDelivery to keep registration open while the application controls
// delivery itself; no Sender is required in that mode. If neither is set, push
// remains disabled.
func WithPushNotifications(cfg push.Config) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		opts.Push = cfg
	}
}

// WithCrossNodeResubscribe is retained for source compatibility. Redis
// SubscribeToTask always uses Redis Streams regardless of enabled.
// Deprecated: cross-node resubscribe is always enabled.
func WithCrossNodeResubscribe(enabled bool) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		opts.CrossNodeResubscribe = true
	}
}
