// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package util

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// This file provides a thin adapter that removes the round-ending ceremony
// from an agent body; see examples/processor for a runnable demonstration.
//
// The core contract stays taskmanager.MessageProcessor: return an event
// channel, close it to end the round. That contract is deliberately minimal —
// one method, a native Go channel — but it leaves three chores with the agent:
// build the handle, spawn the goroutine, and remember Close. The Processor
// shape below moves those three into the adapter, aligned with the official
// A2A executors (a2a-python's AgentExecutor.execute(context, event_queue),
// a2a-go's AgentExecutor.Execute(ctx, reqCtx, queue)):
//
//   - the handle is passed in, framework-side owned;
//   - the body is written straight-line and just emits;
//   - returning ends the round — no Events(), no Close, no goroutine.
//
// End-of-round semantics under this adapter:
//   - return after emitting a terminal state: normal completion;
//   - return with the task suspended (input-required / auth-required): the
//     task awaits a follow-up message;
//   - return a non-nil error: the round is marked failed (unless a terminal
//     state was already emitted, which wins);
//   - return without any conclusion while the task is submitted/working: the
//     framework's close rule marks the task failed — same as the raw contract.

// Processor is the simplified agent-side shape: emit through h, then return.
type Processor interface {
	Process(ctx context.Context, ec *taskmanager.ExecContext, h *taskmanager.TaskHandle) error
}

// AsMessageProcessor adapts a Processor onto the framework's MessageProcessor
// contract. The adapter owns the goroutine and the Close, so the Processor
// body never has to.
func AsMessageProcessor(p Processor) taskmanager.MessageProcessor {
	return processorAdapter{p: p}
}

type processorAdapter struct{ p Processor }

// ProcessMessage implements taskmanager.MessageProcessor.
func (a processorAdapter) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	h := taskmanager.NewTaskHandle(ctx, ec)
	go func() {
		// return == end of round: the adapter closes; the body never does.
		defer h.Close()
		if err := a.p.Process(ctx, ec, h); err != nil {
			// An error with no terminal state emitted fails the round. If the
			// body already emitted a terminal state, that state wins (terminal
			// states are immutable) and this event is discarded.
			_ = h.UpdateTaskState(protocol.TaskStateFailed,
				protocol.NewAgentText("processing failed: "+err.Error()))
		}
	}()
	return h.Events(), nil
}
