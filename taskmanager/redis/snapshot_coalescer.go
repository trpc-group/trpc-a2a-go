// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

type snapshotCoalescer struct {
	manager *TaskManager
	tenant  string
	owner   string
	taskID  string
	runID   string

	mu             sync.Mutex
	pending        *protocol.Task
	dirty          bool
	bufferedEvents int
	timer          *time.Timer
}

func newSnapshotCoalescer(
	m *TaskManager,
	tenant, owner, taskID, runID string,
) *snapshotCoalescer {
	return &snapshotCoalescer{
		manager: m,
		tenant:  tenant,
		owner:   owner,
		taskID:  taskID,
		runID:   runID,
	}
}

// beginCommit serializes artifact commits with snapshot flushes. This makes it
// safe for the flush Lua script to pair the pending Task with the current
// Redis Stream tail: no later Task-changing XADD can land between them.
func (c *snapshotCoalescer) beginCommit() bool {
	cfg := c.manager.options.Persistence
	if !cfg.coalesced() {
		return false
	}
	c.mu.Lock()
	return true
}

func (c *snapshotCoalescer) abortCommit() {
	c.mu.Unlock()
}

func (c *snapshotCoalescer) completeCommit(task *protocol.Task) error {
	defer c.mu.Unlock()
	c.pending = copyTask(task)
	c.dirty = true
	c.bufferedEvents++
	cfg := c.manager.options.Persistence
	maxEvents := cfg.MaxBufferedTaskEvents
	if maxEvents <= 0 {
		maxEvents = 32
	}
	if c.bufferedEvents >= maxEvents {
		return c.flushLocked(context.Background())
	}
	interval := cfg.SnapshotFlushInterval
	if interval <= 0 {
		return nil
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(interval, func() {
			_ = c.Flush(context.Background())
		})
		return nil
	}
	c.timer.Reset(interval)
	return nil
}

func (c *snapshotCoalescer) Flush(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flushLocked(ctx)
}

func (c *snapshotCoalescer) flushLocked(ctx context.Context) error {
	if !c.dirty || c.pending == nil {
		return nil
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	task := c.pending
	if err := c.manager.flushTaskSnapshot(ctx, c.tenant, c.owner, c.runID, task); err != nil {
		return err
	}
	c.pending = nil
	c.dirty = false
	c.bufferedEvents = 0
	return nil
}

func (c *snapshotCoalescer) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

func (c *snapshotCoalescer) hasDirty() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty
}
