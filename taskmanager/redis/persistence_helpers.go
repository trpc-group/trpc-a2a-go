// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func (m *TaskManager) flushTaskSnapshot(
	ctx context.Context,
	tenant, owner, runID string,
	task *protocol.Task,
) error {
	return m.eventTransport.FlushExecutionTaskSnapshot(ctx, tenant, owner, runID, task)
}

func (m *TaskManager) buildTaskCommitOptions(
	allowCreate bool,
) taskCommitOptions {
	opts := taskCommitOptions{}
	cfg := m.options.Persistence
	if !cfg.coalesced() || allowCreate {
		return opts
	}
	opts.skipTaskSet = true
	return opts
}
