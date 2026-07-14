// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package pushdispatch provides the task managers' shared, process-local push
// delivery queue. It is internal so push.Sender remains the only delivery API
// third-party task managers need to implement.
package pushdispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

const (
	defaultConcurrency = 8
	defaultQueueSize   = 256
	maxConcurrency     = 1024
	maxQueueSize       = 1 << 20
)

// ErrClosed is returned when shutdown has started.
var ErrClosed = errors.New("push dispatcher is closed")

type job struct {
	taskID string
	cfg    []byte
	event  []byte
}

// Dispatcher bounds automatic delivery while preserving FIFO order for each
// (taskId, configId). Keys are assigned to stable worker shards; this avoids a
// goroutine per webhook while allowing unrelated webhooks to make progress.
type Dispatcher struct {
	sender push.Sender
	ctx    context.Context
	cancel context.CancelFunc
	queues []chan job
	wg     sync.WaitGroup
	closed atomic.Bool
}

// New constructs a dispatcher. A nil parent uses context.Background.
func New(parent context.Context, sender push.Sender, concurrency, queueSize int) *Dispatcher {
	if parent == nil {
		parent = context.Background()
	}
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	if concurrency > maxConcurrency {
		concurrency = maxConcurrency
	}
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	if queueSize > maxQueueSize {
		queueSize = maxQueueSize
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Dispatcher{
		sender: sender,
		ctx:    ctx,
		cancel: cancel,
		queues: make([]chan job, concurrency),
	}
	base, extra := queueSize/concurrency, queueSize%concurrency
	for i := range d.queues {
		capacity := base
		if i < extra {
			capacity++
		}
		d.queues[i] = make(chan job, capacity)
		d.wg.Add(1)
		go d.run(d.queues[i])
	}
	return d
}

// Enqueue snapshots event and configs before adding one delivery per config.
// It blocks when the bounded queue is full and returns ErrClosed on shutdown.
func (d *Dispatcher) Enqueue(
	configs []protocol.TaskPushNotificationConfig,
	event protocol.StreamResponse,
) error {
	if d == nil || d.sender == nil || len(configs) == 0 {
		return nil
	}
	if d.closed.Load() {
		return ErrClosed
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("snapshot push event: %w", err)
	}
	jobs := make([]job, len(configs))
	for i := range configs {
		cfgJSON, err := json.Marshal(configs[i])
		if err != nil {
			return fmt.Errorf("snapshot push config %q: %w", configs[i].ID, err)
		}
		jobs[i] = job{taskID: configs[i].TaskID, cfg: cfgJSON, event: eventJSON}
	}
	for i := range jobs {
		j := jobs[i]
		queue := d.queues[shard(configs[i], len(d.queues))]
		select {
		case <-d.ctx.Done():
			return ErrClosed
		case queue <- j:
			if d.closed.Load() {
				return ErrClosed
			}
		}
	}
	return nil
}

func (d *Dispatcher) run(queue <-chan job) {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case j := <-queue:
			var cfg protocol.TaskPushNotificationConfig
			if err := json.Unmarshal(j.cfg, &cfg); err != nil {
				log.Warnf("push dispatch: restore config for task %s: %v", j.taskID, err)
				continue
			}
			var event protocol.StreamResponse
			if err := json.Unmarshal(j.event, &event); err != nil {
				log.Warnf("push dispatch: restore event for task %s: %v", j.taskID, err)
				continue
			}
			if err := d.sender.SendPush(d.ctx, cfg, event); err != nil {
				log.Warnf("push dispatch: send config %s for task %s: %v", cfg.ID, j.taskID, err)
			}
		}
	}
}

func shard(cfg protocol.TaskPushNotificationConfig, count int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cfg.TaskID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(cfg.ID))
	// count is capped at maxConcurrency above, so the conversion is safe.
	return int(h.Sum32() % uint32(count)) //nolint:gosec
}

// Close cancels in-flight sends and waits for the fixed workers to exit. Queued
// process-local deliveries are abandoned during shutdown.
func (d *Dispatcher) Close() {
	if d == nil || !d.closed.CompareAndSwap(false, true) {
		return
	}
	d.cancel()
	d.wg.Wait()
}
