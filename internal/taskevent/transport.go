// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskevent defines the internal distribution boundary for Task events.
package taskevent

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// Transport distributes Task events between TaskManager instances.
//
// A cursor is opaque to callers. Implementations must preserve event order.
// CommitTaskEvent and LoadTaskAndCursor provide the atomic boundaries needed so
// a concurrent Task-changing event is either reflected by the loaded snapshot
// and cursor or returned by a subsequent ReadAfter call, never lost between the
// two.
type Transport interface {
	// CommitTaskEvent atomically stores a Task snapshot and the event that
	// produced it.
	CommitTaskEvent(
		ctx context.Context,
		task *protocol.Task,
		event protocol.StreamResponse,
	) error

	// AppendEvent stores an event that does not modify the Task snapshot.
	AppendEvent(
		ctx context.Context,
		taskID string,
		event protocol.StreamResponse,
	) error

	// LoadTaskAndCursor atomically loads the current Task snapshot and the
	// current event cursor. A subscriber uses the snapshot as its initial frame
	// and reads only events committed after that cursor.
	LoadTaskAndCursor(
		ctx context.Context,
		taskID string,
	) (*protocol.Task, string, error)

	// ReadAfter returns an ordered batch strictly after cursor and the cursor to
	// use for the next call. It may block until events arrive or ctx ends. An
	// empty batch is not an error. The returned cursor is never behind the input
	// cursor and may advance past transport records that yielded no valid event.
	ReadAfter(
		ctx context.Context,
		taskID string,
		cursor string,
	) ([]protocol.StreamResponse, string, error)
}
