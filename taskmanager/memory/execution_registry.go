// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"fmt"
	"sync"

	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// executionRegistry is the single writer for live MessageProcessor runs.
// It serializes admission, suspend handoff, cancellation, and Close:
// at most one live slot per scoped task ID.
type executionRegistry struct {
	mu         sync.Mutex
	executions map[scopedID]*execution
	closed     bool
	engines    sync.WaitGroup
}

func newExecutionRegistry() *executionRegistry {
	return &executionRegistry{executions: make(map[scopedID]*execution)}
}

// register publishes the cancellation handle of a starting run. A task admits
// at most one live run; a continuation waits through the previous round's
// short suspend handoff, while any other concurrent round is rejected.
func (r *executionRegistry) register(ctx context.Context, tenant, owner, taskID string, exec *execution) error {
	key := newScopedID(tenant, owner, taskID)
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return taskmanager.ErrInternalError("task manager is closed")
		}
		current, exists := r.executions[key]
		if !exists {
			r.executions[key] = exec
			// Counted under the registry lock so Close (which flips closed first)
			// can never begin waiting before a just-admitted run is counted.
			r.engines.Add(1)
			r.mu.Unlock()
			return nil
		}
		yieldDone := current.yieldDone
		r.mu.Unlock()
		if yieldDone == nil {
			return taskmanager.ErrInvalidParams(fmt.Sprintf("task %s already has an active execution", taskID))
		}
		select {
		case <-yieldDone:
			// The previous round has finished publishing its suspend event. Retry
			// under the lock so Close or another continuation can win cleanly.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// release aborts a registered run whose engine never started: it undoes
// register's registration and engine count.
func (r *executionRegistry) release(tenant, owner, taskID string, exec *execution) {
	r.deregister(tenant, owner, taskID, exec)
	r.engines.Done()
}

// engineDone marks one drain engine finished (paired with register's Add).
func (r *executionRegistry) engineDone() {
	r.engines.Done()
}

// wait blocks until every registered engine has finished.
func (r *executionRegistry) wait() {
	r.engines.Wait()
}

// claimCancelSlot atomically returns the task's live run or — when there is
// none — claims the execution slot with a sentinel, so a no-live cancel's
// CANCELED write gets the same single-writer guarantee as a run: no
// continuation can register (and then write) concurrently with it.
func (r *executionRegistry) claimCancelSlot(
	tenant string,
	owner string,
	taskID string,
) (live *execution, sentinel *execution, yieldDone <-chan struct{}) {
	key := newScopedID(tenant, owner, taskID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if exec, ok := r.executions[key]; ok {
		if exec.yieldDone != nil {
			return nil, nil, exec.yieldDone
		}
		return exec, nil, nil
	}
	sentinel = &execution{cancel: func() {}}
	r.executions[key] = sentinel
	return nil, sentinel, nil
}

// requestCancel linearizes an accepted cancellation against a suspend handoff.
// It returns the handoff channel when yield won, or accepted after publishing
// cancelRequested under the registry lock.
func (r *executionRegistry) requestCancel(
	tenant string,
	owner string,
	taskID string,
	exec *execution,
) (yieldDone <-chan struct{}, accepted bool) {
	key := newScopedID(tenant, owner, taskID)
	r.mu.Lock()
	if r.executions[key] != exec {
		r.mu.Unlock()
		return nil, false
	}
	if exec.yieldDone != nil {
		yieldDone = exec.yieldDone
		r.mu.Unlock()
		return yieldDone, false
	}
	exec.cancelRequested.Store(true)
	r.mu.Unlock()
	exec.cancel()
	return nil, true
}

// deregister removes the handle if it still belongs to this run.
func (r *executionRegistry) deregister(tenant, owner, taskID string, exec *execution) {
	key := newScopedID(tenant, owner, taskID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.executions[key] == exec {
		delete(r.executions, key)
		if exec.yieldDone != nil {
			close(exec.yieldDone)
			exec.yieldDone = nil
		}
	}
}

// beginYield turns an active slot into a short handoff barrier. The slot
// remains owned by exec until its suspend frame has reached every local
// observer, but continuations wait for the handoff instead of failing.
func (r *executionRegistry) beginYield(tenant, owner, taskID string, exec *execution) bool {
	key := newScopedID(tenant, owner, taskID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.executions[key] == exec && exec.yieldDone == nil && !exec.cancelRequested.Load() {
		exec.yieldDone = make(chan struct{})
		return true
	}
	return false
}

// abortYield restores an active slot when committing the suspended state fails.
// Waiters wake and re-evaluate it as an ordinary active run.
func (r *executionRegistry) abortYield(tenant, owner, taskID string, exec *execution) {
	key := newScopedID(tenant, owner, taskID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.executions[key] == exec && exec.yieldDone != nil {
		close(exec.yieldDone)
		exec.yieldDone = nil
	}
}

// live returns the cancellation handle of the task's live run, if any.
func (r *executionRegistry) live(tenant, owner, taskID string) *execution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.executions[newScopedID(tenant, owner, taskID)]
}

// isClosed reports whether shutdown has begun.
func (r *executionRegistry) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// shutdown refuses new runs, cancels every live one, and returns their stream
// pipes so the caller can unblock engines parked on a blocking pipe send.
func (r *executionRegistry) shutdown() []*taskSubscriber {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	pipes := make([]*taskSubscriber, 0, len(r.executions))
	for _, exec := range r.executions {
		exec.cancelRequested.Store(true)
		exec.cancel()
		if exec.pipe != nil {
			pipes = append(pipes, exec.pipe)
		}
	}
	return pipes
}

// len returns the number of registered slots (tests / diagnostics).
func (r *executionRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.executions)
}
