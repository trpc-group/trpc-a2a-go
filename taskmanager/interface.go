// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager defines interfaces and implementations for managing A2A task lifecycles.
package taskmanager

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// TaskHandle provides methods for the agent logic (TaskProcessor) to interact
// with the task manager during processing. It encapsulates the necessary callbacks.
type TaskHandle interface {
	// UpdateStatus updates the task's state and optional message.
	// Returns an error if the task cannot be found or updated.
	UpdateStatus(state protocol.TaskState, msg *protocol.Message) error

	// AddArtifact adds a new artifact to the task.
	// Returns an error if the task cannot be found or updated.
	AddArtifact(artifact protocol.Artifact) error

	// IsStreamingRequest returns true if the task was initiated via a streaming request
	// (OnSendTaskSubscribe) rather than a synchronous request (OnSendTask).
	// This allows the TaskProcessor to adapt its behavior based on the request type.
	IsStreamingRequest() bool

	// GetSessionID returns the session ID for the task.
	// If the task is not associated with a session, it returns nil.
	GetSessionID() *string
}

// TaskProcessor defines the interface for the core agent logic that processes a task.
// Implementations of this interface are injected into a TaskManager.
type TaskProcessor interface {
	// Process executes the specific logic for a task.
	// It receives the task ID, the initial message, and a TaskHandle for callbacks.
	// It should use handle.Context() to check for cancellation.
	// It should report progress and results via handle.UpdateStatus and handle.AddArtifact.
	// Returning an error indicates the processing failed fundamentally.
	Process(ctx context.Context, taskID string, initialMsg protocol.Message, handle TaskHandle) error
}

// TaskProcessorWithStatusUpdate is an optional interface that can be implemented by TaskProcessor
// to receive notifications when the task status changes.
type TaskProcessorWithStatusUpdate interface {
	TaskProcessor
	// OnTaskStatusUpdate is called when the task status changes.
	// It receives the task ID, the new state, and the optional message.
	// It should return an error if the status update fails.
	OnTaskStatusUpdate(ctx context.Context, taskID string, state protocol.TaskState, message *protocol.Message) error
}

// TaskManager defines the interface for managing A2A task lifecycles based on the protocol.
// Implementations handle task creation, updates, retrieval, cancellation, and events,
// delegating the actual processing logic to an injected TaskProcessor.
// This interface corresponds to the Task Service defined in the A2A Specification.
// Exported interface.
type TaskManager interface {
	// OnSendTask handles a request corresponding to the 'tasks/send' RPC method.
	// It creates and potentially starts processing a new task via the TaskProcessor.
	// It returns the initial state of the task, possibly reflecting immediate processing results.
	OnSendTask(ctx context.Context, request protocol.SendTaskParams) (*protocol.Task, error)

	// OnSendTaskSubscribe handles a request corresponding to the 'tasks/sendSubscribe' RPC method.
	// It creates a new task and returns a channel for receiving TaskEvent updates (streaming).
	// It initiates asynchronous processing via the TaskProcessor.
	// The channel will be closed when the task reaches a final state or an error occurs during setup/processing.
	OnSendTaskSubscribe(ctx context.Context, request protocol.SendTaskParams) (<-chan protocol.TaskEvent, error)

	// OnGetTask handles a request corresponding to the 'tasks/get' RPC method.
	// It retrieves the current state of an existing task.
	OnGetTask(ctx context.Context, params protocol.TaskQueryParams) (*protocol.Task, error)

	// OnCancelTask handles a request corresponding to the 'tasks/cancel' RPC method.
	// It requests the cancellation of an ongoing task.
	// This typically involves canceling the context passed to the TaskProcessor.
	// It returns the task state after the cancellation attempt.
	OnCancelTask(ctx context.Context, params protocol.TaskIDParams) (*protocol.Task, error)

	// OnPushNotificationSet handles a request corresponding to the 'tasks/pushNotification/set' RPC method.
	// It configures push notifications for a specific task.
	OnPushNotificationSet(ctx context.Context, params protocol.TaskPushNotificationConfig) (*protocol.TaskPushNotificationConfig, error)

	// OnPushNotificationGet handles a request corresponding to the 'tasks/pushNotification/get' RPC method.
	// It retrieves the current push notification configuration for a task.
	OnPushNotificationGet(ctx context.Context, params protocol.TaskIDParams) (*protocol.TaskPushNotificationConfig, error)

	// OnResubscribe handles a request corresponding to the 'tasks/resubscribe' RPC method.
	// It reestablishes an SSE stream for an existing task.
	OnResubscribe(ctx context.Context, params protocol.TaskIDParams) (<-chan protocol.TaskEvent, error)
}
