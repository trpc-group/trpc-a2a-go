// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

const (
	defaultConcurrency = 8
	defaultQueueSize   = 256
	maxConcurrency     = 1024
	maxQueueSize       = 1 << 20
)

// ErrDispatcherClosed is returned when dispatcher shutdown has started.
var ErrDispatcherClosed = errors.New("push dispatcher is closed")

type job struct {
	taskID     string
	generation string
	cfg        []byte
	event      []byte
}

// Registration is a snapshot of one persisted push registration.
// Generation changes on every create/update so a queued delivery cannot be
// revived by deleting and re-creating the same config ID.
type Registration struct {
	Config     protocol.TaskPushNotificationConfig `json:"config"`
	Generation string                              `json:"generation"`
}

// Dispatcher bounds automatic delivery while preserving FIFO order for each
// (taskId, configId). Keys are assigned to stable worker shards; this avoids a
// goroutine per webhook while allowing unrelated webhooks to make progress.
type Dispatcher struct {
	sender Sender
	ctx    context.Context
	cancel context.CancelFunc
	queues []chan job
	wg     sync.WaitGroup
	closed atomic.Bool
	// isCurrent moves a queued registration into the in-flight state. A false
	// result means the registration was deleted or replaced after enqueue. Once
	// it returns true, a later deletion does not cancel the in-flight SendPush.
	isCurrent func(context.Context, Registration) (bool, error)
}

// NewDispatcher constructs a dispatcher. A nil parent uses context.Background.
func NewDispatcher(
	parent context.Context,
	sender Sender,
	concurrency, queueSize int,
	isCurrent func(context.Context, Registration) (bool, error),
) *Dispatcher {
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
		sender:    sender,
		ctx:       ctx,
		cancel:    cancel,
		queues:    make([]chan job, concurrency),
		isCurrent: isCurrent,
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
// It blocks when the bounded queue is full and returns ErrDispatcherClosed on shutdown.
func (d *Dispatcher) Enqueue(
	registrations []Registration,
	event protocol.StreamResponse,
) error {
	if d == nil || d.sender == nil || len(registrations) == 0 {
		return nil
	}
	if d.closed.Load() {
		return ErrDispatcherClosed
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("snapshot push event: %w", err)
	}
	jobs := make([]job, len(registrations))
	for i := range registrations {
		cfg := registrations[i].Config
		cfgJSON, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("snapshot push config %q: %w", cfg.ID, err)
		}
		jobs[i] = job{
			taskID:     cfg.TaskID,
			generation: registrations[i].Generation,
			cfg:        cfgJSON,
			event:      eventJSON,
		}
	}
	for i := range jobs {
		j := jobs[i]
		queue := d.queues[shard(registrations[i].Config, len(d.queues))]
		select {
		case <-d.ctx.Done():
			return ErrDispatcherClosed
		case queue <- j:
			if d.closed.Load() {
				return ErrDispatcherClosed
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
			// When Close makes both cases ready, select may choose the queue. Do not
			// claim a queued delivery after shutdown has started.
			if d.closed.Load() || d.ctx.Err() != nil {
				return
			}
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
			registration := Registration{Config: cfg, Generation: j.generation}
			if d.isCurrent != nil {
				current, err := d.isCurrent(d.ctx, registration)
				if err != nil {
					log.Warnf("push dispatch: validate config %s for task %s: %v", cfg.ID, j.taskID, err)
					continue
				}
				if !current {
					continue
				}
			}
			d.send(j.taskID, cfg, event)
		}
	}
}

// send contains the user-extensible Sender boundary. Recovery is per job: a
// top-level worker recovery would return from run and permanently strand this
// shard. returned distinguishes panic(nil) from a normal return on Go 1.20.
func (d *Dispatcher) send(
	taskID string,
	cfg protocol.TaskPushNotificationConfig,
	event protocol.StreamResponse,
) {
	returned := false
	defer func() {
		if returned {
			return
		}
		recovered := recover()
		// The panic value is untrusted and may contain the config, including
		// credentials. Log only its type plus the stack.
		log.Errorf("push dispatch: recovered sender panic type %T for config %s task %s\n%s",
			recovered, cfg.ID, taskID, debug.Stack())
	}()
	if err := d.sender.SendPush(d.ctx, cfg, event); err != nil {
		// Sender errors are also untrusted and can embed signed callback URLs or
		// credentials. The sender may log its own safe diagnostic details.
		log.Warnf("push dispatch: send failed for config %s task %s (error type %T)",
			cfg.ID, taskID, err)
	}
	returned = true
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
