// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package retaining defines the persistence and notification contracts used by
// retaining task managers.
package retaining

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

// Cursor is an opaque position in one Task's event journal.
//
// A Store chooses the physical representation. Callers may only preserve a
// Cursor and pass it back to the same Store; they must not parse, order, or
// compare it with cursors from another Task or Store implementation.
type Cursor string

// TaskKey is the complete persistence and isolation scope of one Task.
type TaskKey struct {
	Tenant string
	Owner  string
	ID     string
}

// TaskRecord is a Task snapshot and the Store metadata observed with it.
// Cursor is populated by LoadTaskAndCursor and may be empty after LoadTask.
type TaskRecord struct {
	Task    *protocol.Task
	Version uint64
	Cursor  Cursor
}

// StoredEvent is one journal event and its position.
type StoredEvent struct {
	Cursor Cursor
	Event  protocol.StreamResponse
}

// CommitTaskEventRequest atomically replaces a Task snapshot and appends the
// Status or Artifact event that produced it.
type CommitTaskEventRequest struct {
	Key             TaskKey
	ExpectedVersion uint64
	AllowCreate     bool
	OperationID     string
	Task            *protocol.Task
	Event           protocol.StreamResponse
}

// CommitMessageEventRequest appends a Message event to an existing Task and
// idempotently projects the Message into conversation history.
type CommitMessageEventRequest struct {
	Key             TaskKey
	ExpectedVersion uint64
	OperationID     string
	Message         protocol.Message
}

// Store is the persistence boundary of a retaining task manager.
//
// Implementations must be safe for concurrent use. All input and output
// protocol values cross the boundary by value: implementations must not retain
// caller-owned mutable references or return references to mutable stored state.
//
// Commit methods are operation-idempotent. A repeated OperationID for the same
// request returns the version and cursor produced by its first successful
// commit. The OperationID lookup happens before version and terminal-state
// checks. Reusing an OperationID for different request content returns
// ErrOperationConflict.
type Store interface {
	// LoadTask returns a deep copy of the current Task snapshot and its version.
	LoadTask(ctx context.Context, key TaskKey) (*TaskRecord, error)

	// LoadTaskAndCursor observes the Task snapshot, version, and event-journal
	// tail at one atomic boundary.
	LoadTaskAndCursor(ctx context.Context, key TaskKey) (*TaskRecord, error)

	// CommitTaskEvent atomically updates the Task snapshot and appends its event.
	// It returns the version and cursor produced by this OperationID's first
	// successful commit.
	CommitTaskEvent(ctx context.Context, req CommitTaskEventRequest) (uint64, Cursor, error)

	// CommitMessageEvent first resolves OperationID idempotency, then atomically
	// checks ExpectedVersion and rejects terminal Tasks before appending the
	// Message event derived from Message.
	// Message/history projection is idempotent by MessageID and is retried even
	// when the OperationID already exists. Every Task-associated event advances
	// Version, including an event that does not replace the Task snapshot.
	CommitMessageEvent(ctx context.Context, req CommitMessageEventRequest) (uint64, Cursor, error)

	// ReadTaskEvents returns events strictly after after, in Store order, plus
	// the last physical position inspected. next may advance past malformed
	// backend entries even when no decodable event is returned. It returns
	// ErrCursorExpired rather than silently skipping trimmed events.
	ReadTaskEvents(
		ctx context.Context,
		key TaskKey,
		after Cursor,
		limit int,
	) ([]StoredEvent, Cursor, error)

	// RefreshTaskLease extends a live Task and its owned journal state without
	// recreating a missing or logically expired Task.
	RefreshTaskLease(ctx context.Context, key TaskKey) error

	// SaveMessage idempotently stores message and its conversation index entry by
	// MessageID. Reusing a MessageID for different content returns
	// ErrMessageConflict. It is used for direct Messages and projections outside
	// CommitMessageEvent.
	SaveMessage(ctx context.Context, tenant, owner string, message protocol.Message) error

	// LoadHistory returns at most limit Messages for one conversation in protocol
	// order. A negative limit requests all retained Messages.
	LoadHistory(
		ctx context.Context,
		tenant string,
		owner string,
		contextID string,
		limit int,
	) ([]protocol.Message, error)

	// ListTasks performs backend-native filtering, ordering, and keyset
	// pagination. Returned Tasks do not contain conversation history.
	ListTasks(
		ctx context.Context,
		tenant string,
		owner string,
		params protocol.ListTasksParams,
	) (*protocol.ListTasksResult, error)

	// SavePushConfig creates or replaces a config scoped by key. The Store treats
	// key as authoritative, rejects a payload whose non-empty scope disagrees,
	// atomically verifies that the Task remains visible, assigns server-owned
	// fields, advances Generation, and returns a deep copy of the registration.
	SavePushConfig(
		ctx context.Context,
		key TaskKey,
		config protocol.TaskPushNotificationConfig,
	) (push.Registration, error)

	// GetPushConfig returns one config only while its Task remains visible.
	GetPushConfig(ctx context.Context, key TaskKey, configID string) (push.Registration, error)

	// ListPushConfigs returns the visible configs for key in stable backend order.
	ListPushConfigs(ctx context.Context, key TaskKey) ([]push.Registration, error)

	// DeletePushConfig removes configID from key. Deleting an absent config is a
	// no-op, preserving the existing TaskManager behavior.
	DeletePushConfig(ctx context.Context, key TaskKey, configID string) error

	// Close releases resources owned by this Store wrapper. It must be safe to
	// call more than once.
	Close() error
}
