// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// Event construction helpers for MessageProcessor implementations. They mirror
// the verbs of the former TaskHandler (UpdateTaskState/AddArtifact) as pure
// constructors: TaskID/ContextID are left empty on purpose — the framework
// stamps them from the ExecContext (see the MessageProcessor contract).

// NewStatusUpdate builds a status event for the current round's task.
func NewStatusUpdate(state protocol.TaskState, message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return &protocol.TaskStatusUpdateEvent{
		Status: protocol.TaskStatus{State: state, Message: message},
	}
}

// Working builds a TASK_STATE_WORKING status event. message may be nil.
func Working(message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return NewStatusUpdate(protocol.TaskStateWorking, message)
}

// Completed builds a TASK_STATE_COMPLETED (terminal) status event. message may be nil.
func Completed(message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return NewStatusUpdate(protocol.TaskStateCompleted, message)
}

// Failed builds a TASK_STATE_FAILED (terminal) status event. message may be nil.
func Failed(message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return NewStatusUpdate(protocol.TaskStateFailed, message)
}

// InputRequired builds a TASK_STATE_INPUT_REQUIRED status event: the task
// suspends awaiting a follow-up message (the framework calls ProcessMessage
// again with ExecContext.Task set).
func InputRequired(message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return NewStatusUpdate(protocol.TaskStateInputRequired, message)
}

// AuthRequired builds a TASK_STATE_AUTH_REQUIRED status event: the task
// suspends awaiting a follow-up message (the framework calls ProcessMessage
// again with ExecContext.Task set).
func AuthRequired(message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return NewStatusUpdate(protocol.TaskStateAuthRequired, message)
}

// Canceled builds a TASK_STATE_CANCELED (terminal) status event, for a
// MessageProcessor that concludes cancellation itself. message may be nil.
func Canceled(message *protocol.Message) *protocol.TaskStatusUpdateEvent {
	return NewStatusUpdate(protocol.TaskStateCanceled, message)
}

// NewArtifactUpdate builds an artifact event for the current round's task.
// lastChunk marks the final chunk of this artifact.
func NewArtifactUpdate(artifact protocol.Artifact, lastChunk bool) *protocol.TaskArtifactUpdateEvent {
	return &protocol.TaskArtifactUpdateEvent{
		Artifact:  artifact,
		LastChunk: &lastChunk,
	}
}

// ReplyText builds an agent text message, for pure-message replies or as the
// message attached to a status event.
func ReplyText(text string) *protocol.Message {
	message := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]*protocol.Part{protocol.NewTextPart(text)},
	)
	return &message
}

// Events returns a closed channel pre-filled with the given events: the
// return value for a fully synchronous ProcessMessage (no goroutine needed).
// Calling it with no events yields an empty round, which OnSendMessage rejects
// with an internal error — a MessageProcessor must produce at least one event.
func Events(events ...protocol.StreamEvent) <-chan protocol.StreamEvent {
	out := make(chan protocol.StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}
