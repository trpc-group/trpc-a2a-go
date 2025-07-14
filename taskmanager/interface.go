// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager defines interfaces and implementations for managing A2A task lifecycles.
package taskmanager

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// ProcessOptions contains configuration options for processing messages
type ProcessOptions struct {
	// Blocking indicates whether this is a blocking request
	// If true, the user should wait for processing completion before returning the final result
	// If false, the user can immediately return the initial state and update later through other means
	Blocking bool

	// HistoryLength indicates the length of historical messages requested by the client
	HistoryLength int

	// PushNotificationConfig contains push notification configuration
	PushNotificationConfig *protocol.PushNotificationConfig

	// AcceptedOutputModes is the list of accepted output modes.
	AcceptedOutputModes []string

	// Streaming indicates whether this is a streaming request
	// If true, the user should return event streams through the StreamingEvents channel
	// If false, the user should return a single result through Result
	Streaming bool
}

// CancellableTask is a task that can be cancelled
type CancellableTask interface {
	// Task returns the original task.
	Task() *protocol.Task

	// Cancel cancels the task.
	Cancel()
}

// TaskHandler provides methods for the agent logic (MessageProcessor) to interact
// with the task manager during processing. It encapsulates the necessary callbacks.
type TaskHandler interface {
	// BuildTask creates a new task and returns the task ID.
	// If ContextID is not set, it will assign a contextID from request or generate a new one
	BuildTask(specificTaskID *string, contextID *string) (string, error)

	// UpdateTaskState updates the task's state and returns the updated task ID.
	UpdateTaskState(taskID *string, state protocol.TaskState, message *protocol.Message) error

	// AddArtifact adds an artifact to the specified task.
	AddArtifact(taskID *string, artifact protocol.Artifact, isFinal bool, needMoreData bool) error

	// SubscribeTask subscribes to the task and returns the task subscriber.
	SubscribeTask(taskID *string) (TaskSubscriber, error)

	// GetTask returns the task by taskID. Returns an error if the task cannot be found.
	GetTask(taskID *string) (CancellableTask, error)

	// CleanTask cleans up the task from storage.
	// CleanTask should be called when the task is no longer needed.
	CleanTask(taskID *string) error

	// GetMessageHistory returns the conversation history for the current context.
	GetMessageHistory() []protocol.Message

	// GetContextID returns the context ID of the current message, if any.
	GetContextID() string
}

// MessageProcessingResult represents the result of processing a message.
type MessageProcessingResult struct {
	// Result can be Message or Task
	// When Streaming=false, use this field
	// The framework will automatically handle whether to wait for the task to complete based on ProcessOptions.Blocking
	Result protocol.UnaryMessageResult

	// StreamingEvents streaming event tunnel
	// When Streaming=true, use this field
	// Message、Task、TaskStatusUpdateEvent、TaskArtifactUpdateEvent is allowed to sent.
	StreamingEvents TaskSubscriber
}

// TaskSubscriber is a subscriber for a task
type TaskSubscriber interface {
	// Send sends an event to the task subscriber, could be blocked if the channel is full
	// If the contextID is not set, it will generate a new contextID automatically
	Send(event protocol.StreamingMessageEvent) error

	// Channel returns the channel of the task subscriber
	Channel() <-chan protocol.StreamingMessageEvent

	// Closed returns true if the task subscriber is closed
	Closed() bool

	// Close close the task subscriber
	Close()
}

// MessageProcessor defines the interface for processing A2A messages.
// This interface should be implemented by users to define their agent's behavior.
type MessageProcessor interface {
	// ProcessMessage processes an incoming message and returns the result.
	//
	// Processing modes:
	// 1. Non-streaming (options.Streaming=false):
	//    - Return MessageProcessingResult.Result (Message or Task)
	//    - The framework directly returns the user's result (if it's a non-final Task state, a reminder log will be printed)
	//
	// 2. Streaming (options.Streaming=true):
	//    - Return MessageProcessingResult.StreamingEvents channel
	//    - Multiple types of events can be sent through the channel:
	//      * protocol.Message - direct message reply
	//      * protocol.Task - task status
	//      * protocol.TaskStatusUpdateEvent - task status update
	//      * protocol.TaskArtifactUpdateEvent - artifact update
	//    - Users are responsible for closing the channel to end streaming transmission
	//
	// Parameters:
	//   - ctx: Request context
	//   - message: The incoming message to process
	//   - options: Processing options including blocking, streaming, history length, etc.
	//   - taskHandler: Task handler for accessing context, history, and task operations
	//
	// Returns:
	//   - MessageProcessingResult: Contains the result or streaming channel
	//   - error: Any error that occurred during processing
	ProcessMessage(
		ctx context.Context,
		message protocol.Message,
		options ProcessOptions,
		taskHandler TaskHandler,
	) (*MessageProcessingResult, error)
}

// TaskManager defines the interface for managing A2A task lifecycles based on the protocol.
// Implementations handle task creation, updates, retrieval, cancellation, and events,
// delegating the actual processing logic to an injected MessageProcessor.
// This interface corresponds to the Task Service defined in the A2A Specification.
// Exported interface.
type TaskManager interface {

	// OnSendMessage handles a request corresponding to the 'message/send' RPC method.
	// It creates and potentially starts processing a new message via the MessageProcessor.
	// It returns the initial state of the message, possibly reflecting immediate processing results.
	OnSendMessage(
		ctx context.Context,
		request protocol.SendMessageParams,
	) (*protocol.MessageResult, error)

	// OnSendMessageStream handles a request corresponding to the 'message/stream' RPC method.
	// It creates a new message and returns a channel for receiving MessageEvent updates (streaming).
	// It initiates asynchronous processing via the MessageProcessor.
	// The channel will be closed when the message reaches a final state or an error occurs during setup/processing.
	OnSendMessageStream(
		ctx context.Context,
		request protocol.SendMessageParams,
	) (<-chan protocol.StreamingMessageEvent, error)

	// OnGetTask handles a request corresponding to the 'tasks/get' RPC method.
	// It retrieves the current state of an existing task.
	OnGetTask(
		ctx context.Context,
		params protocol.TaskQueryParams,
	) (*protocol.Task, error)

	// OnCancelTask handles a request corresponding to the 'tasks/cancel' RPC method.
	// It requests the cancellation of an ongoing task.
	// This typically involves canceling the context passed to the MessageProcessor.
	// It returns the task state after the cancellation attempt.
	OnCancelTask(
		ctx context.Context,
		params protocol.TaskIDParams,
	) (*protocol.Task, error)

	// OnPushNotificationSet handles a request corresponding to the 'tasks/pushNotification/set' RPC method.
	// It configures push notifications for a specific task.
	OnPushNotificationSet(
		ctx context.Context,
		params protocol.TaskPushNotificationConfig,
	) (*protocol.TaskPushNotificationConfig, error)

	// OnPushNotificationGet handles a request corresponding to the 'tasks/pushNotification/get' RPC method.
	// It retrieves the current push notification configuration for a task.
	OnPushNotificationGet(
		ctx context.Context,
		params protocol.TaskIDParams,
	) (*protocol.TaskPushNotificationConfig, error)

	// OnResubscribe handles a request corresponding to the 'tasks/resubscribe' RPC method.
	// It reestablishes an SSE stream for an existing task.
	OnResubscribe(
		ctx context.Context,
		params protocol.TaskIDParams,
	) (<-chan protocol.StreamingMessageEvent, error)

	// deprecated
	// OnSendTask handles a request corresponding to the 'tasks/send' RPC method.
	// It creates and potentially starts processing a new task via the MessageProcessor.
	// It returns the initial state of the task, possibly reflecting immediate processing results.
	OnSendTask(
		ctx context.Context,
		request protocol.SendTaskParams,
	) (*protocol.Task, error)

	// deprecated
	// OnSendTaskSubscribe handles a request corresponding to the 'tasks/sendSubscribe' RPC method.
	// It creates a new task and returns a channel for receiving TaskEvent updates (streaming).
	// It initiates asynchronous processing via the MessageProcessor.
	// The channel will be closed when the task reaches a final state or an error occurs during setup/processing.
	OnSendTaskSubscribe(
		ctx context.Context,
		request protocol.SendTaskParams,
	) (<-chan protocol.TaskEvent, error)
}
