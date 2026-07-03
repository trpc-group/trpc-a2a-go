// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// TaskHandle keeps the former processor's writing style on top of the MessageProcessor
// contract: the old TaskHandler verbs (UpdateTaskState/AddArtifact/reads) are
// expressed over the one event channel, so v0.x processor bodies port with
// minimal edits and minimal relearning. Everything still flows through the
// event stream — the framework's persistence, ordering and fan-out guarantees
// apply unchanged, and the removed ambient authority (foreign-task writes,
// SubscribeTask, CleanTask) does not come back.
//
// It is constructed by the user and bound to one ProcessMessage round:
//
//	func (p *proc) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
//		h := taskmanager.NewTaskHandle(ctx, ec, 8)
//		go func() {
//			defer h.Close()
//			h.UpdateTaskState(protocol.TaskStateWorking, nil)
//			// ... work ...
//			h.AddArtifact(artifact, true)
//			h.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText("done"))
//		}()
//		return h.Events(), nil
//	}
//
// The framework starts draining only after ProcessMessage returns, so emitting
// from the same goroutine as ProcessMessage blocks once the buffer fills.
// Emit from a goroutine (as above), or use the package-level Events helper for
// fully synchronous replies. A blocked emit is not a permanent deadlock: it
// fails with the ctx error once the round is canceled (CancelTask / manager
// teardown), releasing the goroutine.
type TaskHandle struct {
	ctx context.Context
	ec  *ExecContext
	out chan protocol.StreamEvent
}

// NewTaskHandle builds a handle for one ProcessMessage round. ctx is the
// round's context as passed to ProcessMessage: an emit blocked on a full
// buffer aborts when it is canceled. buffer sizes the event channel; because
// the framework drains only after ProcessMessage returns, emitting beyond the
// buffer from that same goroutine blocks — emit from a separate goroutine (as
// in the type example).
func NewTaskHandle(ctx context.Context, ec *ExecContext, buffer int) *TaskHandle {
	if ctx == nil {
		ctx = context.Background()
	}
	if buffer < 0 {
		buffer = 0
	}
	return &TaskHandle{ctx: ctx, ec: ec, out: make(chan protocol.StreamEvent, buffer)}
}

// Events returns the channel to hand back from ProcessMessage.
func (h *TaskHandle) Events() <-chan protocol.StreamEvent { return h.out }

// Close ends the round (the former subscriber.Close/stream end). Call it
// exactly once, from the emitting goroutine: using the handle after Close
// sends on a closed channel and panics.
func (h *TaskHandle) Close() { close(h.out) }

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

// emit hands one event to the framework. A send that can proceed always does
// (a canceled round is still drained, and a post-cancel terminal event must
// win per the contract); only a send with no consumer fails, with the ctx
// error, once the round is canceled — so a mis-ported synchronous emitter is
// rescuable by CancelTask instead of deadlocking forever.
func (h *TaskHandle) emit(event protocol.StreamEvent) error {
	select {
	case h.out <- event:
		return nil
	default:
	}
	select {
	case h.out <- event:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

// UpdateTaskState emits a status event for this round's task
// (former TaskHandler.UpdateTaskState; no taskID argument — one round drives
// exactly its own task, and the framework stamps the IDs).
func (h *TaskHandle) UpdateTaskState(state protocol.TaskState, message *protocol.Message) error {
	return h.emit(NewStatusUpdate(state, message))
}

// AddArtifact emits an artifact event for this round's task
// (former TaskHandler.AddArtifact; lastChunk marks the artifact's final chunk).
func (h *TaskHandle) AddArtifact(artifact protocol.Artifact, lastChunk bool) error {
	return h.emit(NewArtifactUpdate(artifact, lastChunk))
}

// Reply emits a direct message reply (the former pure-Message result path).
func (h *TaskHandle) Reply(message *protocol.Message) error {
	return h.emit(message)
}
