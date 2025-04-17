// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"time"
)

// Option is a function that configures the RedisTaskManager.
type Option func(*TaskManager)

// WithExpiration sets the expiration time for Redis keys.
func WithExpiration(expiration time.Duration) Option {
	return func(o *TaskManager) {
		o.expiration = expiration
	}
}
