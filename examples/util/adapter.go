// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package util

import (
	"context"
	"fmt"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// NewMessageProcessor adapts a mock Agent into a taskmanager.MessageProcessor,
// mapping the agent's event stream onto the A2A task lifecycle:
//
//   - an empty input replies with a validation Message and creates no task;
//   - otherwise the task opens in the working state, each partial event becomes
//     a working-state progress update, and the final event becomes an artifact
//     plus a completed status carrying the full reply.
//
// This is the reusable bridge the examples use to drive A2A from an agent — the
// same shape a real trpc-agent-go adapter would take.
func NewMessageProcessor(agent Agent) taskmanager.MessageProcessor {
	return &agentProcessor{agent: agent}
}

type agentProcessor struct {
	agent Agent
}

// ProcessMessage implements taskmanager.MessageProcessor.
func (p *agentProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)

	input := firstText(ec.Message.Parts)
	if input == "" {
		// A pure-message reply: no task comes into existence this round.
		handle.Reply(protocol.NewAgentText("input message must contain text"))
		handle.Close()
		return handle.Events(), nil
	}

	go func() {
		defer handle.Close()
		p.run(ctx, handle, input)
	}()
	return handle.Events(), nil
}

// run drives one agent run to a terminal task state.
func (p *agentProcessor) run(ctx context.Context, handle *taskmanager.TaskHandle, input string) {
	events, err := p.agent.Run(ctx, input)
	if err != nil {
		handle.UpdateTaskState(protocol.TaskStateFailed,
			protocol.NewAgentText(fmt.Sprintf("agent failed to start: %v", err)))
		return
	}

	// The first task event opens the task (the framework creates it lazily).
	handle.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText("processing..."))

	var full string
	var runErr error
	for ev := range events { // always drain the agent stream until it closes
		switch {
		case ev.Err != nil:
			runErr = ev.Err
		case ev.Done:
			full = ev.Content
		case ev.Partial && ev.Content != "":
			// Stream progress as working-state updates.
			handle.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText(ev.Content))
		}
	}

	if runErr != nil {
		// A canceled ctx is not a failure: closing without a terminal state lets
		// the framework persist CANCELED on our behalf. Other errors fail.
		if ctx.Err() != nil {
			return
		}
		handle.UpdateTaskState(protocol.TaskStateFailed,
			protocol.NewAgentText(fmt.Sprintf("agent failed: %v", runErr)))
		return
	}

	// Emit the full reply as an artifact, then complete with it as a Message so
	// unary callers and multi-turn history both see the result.
	handle.AddArtifact(*protocol.NewArtifactWithID(
		stringPtr("result"),
		stringPtr("Agent result"),
		[]*protocol.Part{protocol.NewTextPart(full)},
	), true)
	handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(full))
}

// firstText returns the first non-empty text content across the parts.
func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}

// stringPtr returns a pointer to s.
func stringPtr(s string) *string { return &s }
