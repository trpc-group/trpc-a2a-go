// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"errors"
	"sync"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// errRoundClosed is returned by emits made after Close: the round has ended.
var errRoundClosed = errors.New("taskmanager: TaskHandle used after Close")

// liveBuffer is the extra capacity of the event channel for emits made after
// Events(): small live bursts proceed without waiting for a consumer.
const liveBuffer = 8

// TaskHandle keeps the former processor's writing style on top of the MessageProcessor
// contract: the TaskHandler-style verbs (UpdateTaskState/AddArtifact/
// AppendArtifact/reads) are
// expressed over the one event channel, so v0.x processor bodies port with
// minimal edits and minimal relearning. Everything still flows through the
// event stream — the framework's persistence, ordering and fan-out guarantees
// apply unchanged, and the removed ambient authority (foreign-task writes,
// SubscribeTask, CleanTask) does not come back.
//
// A synchronous v0.x-style body works as-is: emits made before Events() is
// called are buffered internally without limit and never block, so the whole
// round can run in the ProcessMessage goroutine:
//
//	func (p *proc) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
//		h := taskmanager.NewTaskHandle(ctx, ec)
//		defer h.Close()
//		h.UpdateTaskState(protocol.TaskStateWorking, nil)
//		// ... work ...
//		h.AddArtifact(artifact, true)
//		h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
//		return h.Events(), nil
//	}
//
// For live streaming, emit from a spawned goroutine and call Events() as the
// return expression, exactly like the sync form. An emit made after Events()
// hands the event straight to the framework; if it has to wait for a consumer
// it aborts with the ctx error once the round is canceled (CancelTask /
// manager teardown) instead of blocking forever.
//
// Close ends the round; call it from the goroutine that emits (the sync
// pattern is `defer h.Close()` — the deferred close runs after the return
// value is evaluated, so the framework sees every event and then the end of
// the round).
type TaskHandle struct {
	ctx context.Context
	ec  *ExecContext

	mu     sync.Mutex
	queue  []protocol.StreamEvent // emits before Events(), flushed into out
	out    chan protocol.StreamEvent
	closed bool
}

// NewTaskHandle builds a handle for one ProcessMessage round. ctx is the
// round's context as passed to ProcessMessage: a live emit (made after
// Events()) that has to wait for a consumer aborts when it is canceled.
func NewTaskHandle(ctx context.Context, ec *ExecContext) *TaskHandle {
	if ctx == nil {
		ctx = context.Background()
	}
	return &TaskHandle{ctx: ctx, ec: ec}
}

// Events returns the channel to hand back from ProcessMessage. Everything
// emitted so far is flushed into it; from then on emits go straight to the
// channel. Call it as the return expression (see the type example).
func (h *TaskHandle) Events() <-chan protocol.StreamEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.out == nil {
		h.out = make(chan protocol.StreamEvent, len(h.queue)+liveBuffer)
		for _, event := range h.queue {
			h.out <- event
		}
		h.queue = nil
		if h.closed {
			close(h.out)
		}
	}
	return h.out
}

// Close ends the round (the former subscriber.Close / stream end). It is
// idempotent, and emits after Close fail with an error instead of panicking —
// but it must not race a live emit from another goroutine: close from the
// emitting goroutine.
func (h *TaskHandle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.out != nil {
		close(h.out)
	}
}

// TaskID returns the framework-assigned task ID for this round
// (the former BuildTask return value; creation itself is now lazy).
func (h *TaskHandle) TaskID() string { return h.ec.TaskID }

// GetContextID returns the conversation context ID (former TaskHandler.GetContextID).
func (h *TaskHandle) GetContextID() string { return h.ec.ContextID }

// GetMessageHistory returns the conversation history snapshot
// (former TaskHandler.GetMessageHistory).
func (h *TaskHandle) GetMessageHistory() []protocol.Message { return h.ec.History }

// GetTask returns the current task snapshot on a continuation round, nil on
// the first round (former TaskHandler.GetTask, scoped to this round's task).
func (h *TaskHandle) GetTask() *protocol.Task { return h.ec.Task }

// emit hands one event to the framework. Before Events() it appends to the
// internal buffer and always succeeds. After Events() it sends on the live
// channel: a send that can proceed always does (a canceled round is still
// drained, and a post-cancel terminal event must win per the contract); only
// a send with no consumer fails, with the ctx error, once the round is
// canceled.
func (h *TaskHandle) emit(event protocol.StreamEvent) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errRoundClosed
	}
	if h.out == nil {
		h.queue = append(h.queue, event)
		h.mu.Unlock()
		return nil
	}
	out := h.out
	h.mu.Unlock()
	select {
	case out <- event:
		return nil
	default:
	}
	select {
	case out <- event:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

// UpdateTaskState emits a status event for this round's task
// (former TaskHandler.UpdateTaskState; no taskID argument — one round drives
// exactly its own task, and the framework stamps the IDs). The state drives
// the round's lifecycle: completed/failed/canceled/rejected are terminal, and
// input-required/auth-required suspend the task awaiting a follow-up message
// (the framework calls ProcessMessage again with ExecContext.Task set).
func (h *TaskHandle) UpdateTaskState(state protocol.TaskState, message *protocol.Message) error {
	return h.emit(&protocol.TaskStatusUpdateEvent{
		Status: protocol.TaskStatus{State: state, Message: message},
	})
}

// AddArtifact emits a new artifact (or replaces an existing artifact with the
// same ArtifactID) for this round's task. Use AppendArtifact for continuation
// chunks. lastChunk marks the final chunk of the artifact.
func (h *TaskHandle) AddArtifact(artifact protocol.Artifact, lastChunk bool) error {
	return h.emit(&protocol.TaskArtifactUpdateEvent{
		Artifact:  artifact,
		LastChunk: &lastChunk,
	})
}

// AppendArtifact appends a continuation chunk to an artifact already emitted
// with AddArtifact. Reuse the same ArtifactID across calls; the framework
// concatenates the incoming parts onto the existing artifact. lastChunk marks
// the final chunk of the artifact.
func (h *TaskHandle) AppendArtifact(artifact protocol.Artifact, lastChunk bool) error {
	appendChunk := true
	return h.emit(&protocol.TaskArtifactUpdateEvent{
		Artifact:  artifact,
		Append:    &appendChunk,
		LastChunk: &lastChunk,
	})
}

// Reply emits a complete direct message reply (the former pure-Message result
// path). It must be the first and only event shape in the round; do not combine
// it with task status or artifact events.
func (h *TaskHandle) Reply(message *protocol.Message) error {
	return h.emit(message)
}
