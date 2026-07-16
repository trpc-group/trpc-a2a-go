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

	// TaskSubscriberBufSize is the buffer size for task subscriber channels.
	TaskSubscriberBufSize int

	// TaskSubscriberBlockingSend enables blocking send for local task subscribers
	// and request pipes. Cross-node resubscribe tailers always use blocking send:
	// their Redis reader can wait without blocking a task producer.
	TaskSubscriberBlockingSend bool

	// Push configures push-notification delivery (see push.Config). Push is
	// enabled by either a Sender for automatic delivery or ManualDelivery for
	// application-owned delivery.
	Push push.Config

	// CrossNodeResubscribe, when true, mirrors each task's events onto a per-task
	// Redis stream and serves OnResubscribe by tailing that stream. This makes
	// SubscribeToTask work across instances (a reconnect landing on a different
	// node than the one running the task), at the cost of one XADD per event.
	// Each task stream retains approximately the latest 10,000 events.
	// When false (default) resubscribe is served from the in-process subscriber
	// map only — correct for single-instance deployments.
	CrossNodeResubscribe bool
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

// WithTaskSubscriberBlockingSend sets blocking send for local task subscribers
// and request pipes. Cross-node resubscribe tailers always block independently.
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

// WithCrossNodeResubscribe enables cross-instance SubscribeToTask by mirroring
// each task's events onto a per-task Redis stream. Enable it on every replica
// sharing Redis when a resubscribe may land on a different instance than the one
// running the task. It does not distribute execution, continuation, or cancel
// requests between replicas. Each task stream retains approximately the latest
// 10,000 events.
func WithCrossNodeResubscribe(enabled bool) TaskManagerOption {
	return func(opts *TaskManagerOptions) {
		opts.CrossNodeResubscribe = enabled
	}
}
