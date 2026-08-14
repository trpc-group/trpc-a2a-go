// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package retaining

import "errors"

// Store errors are transport-neutral. The retaining Manager maps them to the
// public taskmanager error model at the protocol boundary.
var (
	// ErrTaskNotFound reports a missing or logically expired Task.
	ErrTaskNotFound = errors.New("retaining store: task not found")
	// ErrVersionConflict reports a failed expected-version comparison.
	ErrVersionConflict = errors.New("retaining store: task version conflict")
	// ErrTaskTerminal reports an attempt to append a new event to a terminal Task.
	ErrTaskTerminal = errors.New("retaining store: task is terminal")
	// ErrOperationConflict reports reuse of an OperationID for different content.
	ErrOperationConflict = errors.New("retaining store: operation id conflict")
	// ErrOperationExpired reports that an idempotent result left the operation journal.
	ErrOperationExpired = errors.New("retaining store: operation result expired")
	// ErrCommitUncertain asks the Manager to retry with the original OperationID.
	ErrCommitUncertain = errors.New("retaining store: commit result uncertain")
	// ErrCursorExpired reports that an event cursor fell behind retention.
	ErrCursorExpired = errors.New("retaining store: event cursor expired")
	// ErrPushConfigNotFound reports a missing push configuration for an existing Task.
	ErrPushConfigNotFound = errors.New("retaining store: push config not found")
	// ErrStoreClosed reports an operation attempted after Store.Close.
	ErrStoreClosed = errors.New("retaining store: closed")
)
