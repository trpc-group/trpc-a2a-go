// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package executionlease defines the Redis TaskManager's internal distributed
// execution ownership and cancellation contract.
package executionlease

import (
	"context"
	"errors"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

var (
	// ErrStale means the caller no longer owns the execution lease.
	ErrStale = errors.New("execution lease is stale")
	// ErrCancelRequested means a cancel request fenced a write from the current
	// execution; its close rule should persist a CANCELED Task.
	ErrCancelRequested = errors.New("execution cancellation requested")
)

// Backend provides the atomic operations required to coordinate one Task
// execution across Redis TaskManager nodes.
type Backend interface {
	AcquireExecution(
		ctx context.Context,
		tenant, owner, taskID, runID string,
	) (task *protocol.Task, acquired bool, cancelPending bool, err error)
	RequestExecutionCancel(
		ctx context.Context,
		tenant, owner, taskID string,
	) (task *protocol.Task, committed bool, acknowledged bool, err error)
	AcknowledgeExecutionCancel(
		ctx context.Context,
		tenant, owner, taskID, runID string,
	) error
	CheckAndRenewExecution(
		ctx context.Context,
		tenant, owner, taskID, runID string,
	) (owned bool, cancelRequested bool, err error)
	ReleaseExecution(
		ctx context.Context,
		tenant, owner, taskID, runID string,
	) error
	CommitExecutionTaskEvent(
		ctx context.Context,
		tenant, owner string,
		runID string,
		task *protocol.Task,
		event protocol.StreamResponse,
		allowCreate bool,
		release bool,
		terminalCanWinCancel bool,
	) error
	AppendExecutionEvent(
		ctx context.Context,
		tenant, owner, taskID, runID string,
		event protocol.StreamResponse,
	) error
}
