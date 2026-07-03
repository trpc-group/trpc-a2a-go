// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager defines interfaces and implementations for managing A2A task lifecycles.
package taskmanager

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// ExecContext is the read-only snapshot of one incoming message for a
// MessageProcessor to process. It is a struct (not an interface) on purpose:
// adding fields later is a non-breaking change.
type ExecContext struct {
	// TaskID is pre-allocated by the framework. Whether a task actually comes
	// into existence depends on the MessageProcessor: the framework creates (and
	// persists) the task lazily when the first task event is emitted. On a
	// continuation (see Task) it is the existing task's ID.
	TaskID string

	// Task is non-nil on a continuation: when a client follows up on a task in
	// the input-required or auth-required state by sending another message
	// with the same taskId, Task carries the current task snapshot. It is nil
	// on the first round.
	Task *protocol.Task

	// Message is the incoming message to process.
	Message protocol.Message

	// ContextID is the conversation context ID. When the request does not
	// carry one, the framework generates it before invoking the MessageProcessor.
	ContextID string

	// Tenant is the A2A v1.0 tenant the request is addressed to (from the
	// request body). It lets one process host multiple agents: the MessageProcessor
	// dispatches on it. Empty for single-agent deployments.
	Tenant string

	// History is a snapshot of the conversation history for ContextID,
	// truncated per the manager's configuration. (The client-requested
	// historyLength applies to the task returned in responses, not to this
	// snapshot.)
	History []protocol.Message

	// AcceptedOutputModes is the client's declared list of accepted output
	// modes, when provided.
	AcceptedOutputModes []string

	// PushConfig is the push-notification configuration carried inline on the
	// send request (configuration.pushNotificationConfig), when provided. The
	// framework passes it through without registering it: honoring it is the
	// MessageProcessor's decision.
	PushConfig *protocol.TaskPushNotificationConfig
}

// MessageProcessor is the single interface implemented by users to define an agent's
// behavior: process one message and report progress by sending events on the
// returned channel.
//
// Allowed event types:
//   - *protocol.Message: a direct reply. No task comes into existence for a
//     pure-message exchange.
//   - *protocol.TaskStatusUpdateEvent, *protocol.TaskArtifactUpdateEvent:
//     drive the task identified by ExecContext.TaskID. The framework owns the
//     task lifecycle: it creates the task on the first task event, persists
//     every event before broadcasting it to subscribers, and derives the
//     unary (message/send) result from the stream.
//   - *protocol.Task is NOT allowed: task snapshots are materialized by the
//     framework from the event stream. Emitting one is a contract violation.
//
// Events may leave TaskID/ContextID empty; the framework stamps them. Filling
// them with values different from the ExecContext's is a contract violation
// (the framework marks the task failed and discards the remaining events; the
// channel is still drained).
//
// Sending an event hands it off to the framework: the processor must not
// retain and mutate an event (or its Parts/Message) after sending it.
//
// Closing the channel ends the round:
//   - with the task in a terminal state, or after a pure-message reply: a
//     normal end;
//   - in input-required / auth-required: the task stays suspended awaiting a
//     follow-up message (the framework will call ProcessMessage again with
//     ExecContext.Task set);
//   - in submitted / working: the framework marks the task failed —
//     finishing without a conclusion is a MessageProcessor bug;
//   - after a CancelTask-triggered ctx cancellation: closing without a
//     terminal state leads the framework to mark the task CANCELED on the
//     processor's behalf.
//
// A terminal or suspend-state status event ends the round's writes early: a
// suspend event yields the task — the framework immediately admits a
// continuation, so this round no longer owns the task and any events it emits
// afterwards are discarded (close the channel after suspending). The
// message/stream response stream ends at the terminal or suspend frame; the
// channel itself is still drained until closed.
//
// ctx is canceled when the task is canceled via CancelTask. A client
// disconnect does NOT cancel ctx: the work keeps running and its results
// remain retrievable (GetTask / SubscribeToTask). The framework always drains
// the channel until it is closed, so senders never leak.
//
// The framework starts consuming the channel only after ProcessMessage
// returns: sends beyond the channel buffer from inside ProcessMessage itself
// block forever. Emit from a goroutine, or use the package-level Events
// helper for fully synchronous replies.
//
// Returning a non-nil error means the round failed to start: no events are
// consumed and the error is mapped to a JSON-RPC error. To report a business
// failure, emit a TASK_STATE_FAILED status event and close the channel
// instead.
type MessageProcessor interface {
	ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}

// TaskManager defines the interface for managing A2A task lifecycles based on the protocol.
// Implementations handle task creation, updates, retrieval, cancellation, and events,
// delegating the agent logic to an injected MessageProcessor.
// This interface corresponds to the Task Service defined in the A2A Specification.
type TaskManager interface {

	// OnSendMessage handles a request corresponding to the 'message/send' RPC method.
	// It invokes the MessageProcessor and derives the result from the emitted events:
	// the final task snapshot when task events were emitted, otherwise the last
	// message. With returnImmediately=true it returns as soon as the first
	// decisive event is persisted, while execution continues in the background.
	OnSendMessage(
		ctx context.Context,
		request protocol.SendMessageParams,
	) (*protocol.SendMessageResponse, error)

	// OnSendMessageStream handles a request corresponding to the 'message/stream' RPC method.
	// It invokes the MessageProcessor and returns a channel that carries every emitted
	// event (persisted before delivery). The channel is closed when the round
	// ends; setup errors are returned directly instead.
	OnSendMessageStream(
		ctx context.Context,
		request protocol.SendMessageParams,
	) (<-chan protocol.StreamResponse, error)

	// OnGetTask handles a request corresponding to the 'tasks/get' RPC method.
	// It retrieves the current state of an existing task.
	OnGetTask(
		ctx context.Context,
		params protocol.TaskQueryParams,
	) (*protocol.Task, error)

	// OnCancelTask handles a request corresponding to the 'tasks/cancel' RPC method.
	// With a live execution it cancels the context passed to the running
	// MessageProcessor and returns the current (possibly still non-terminal) task
	// snapshot: the terminal CANCELED state is persisted by the close rule when
	// the MessageProcessor winds down, and a terminal state the MessageProcessor emits
	// itself wins. Without a live execution CANCELED is persisted before returning.
	OnCancelTask(
		ctx context.Context,
		params protocol.TaskIDParams,
	) (*protocol.Task, error)

	// OnListTasks handles a request corresponding to the v1.0 'ListTasks' RPC method.
	// It returns tasks visible to the caller, with optional filtering and pagination.
	OnListTasks(
		ctx context.Context,
		params protocol.ListTasksParams,
	) (*protocol.ListTasksResult, error)

	// OnPushNotificationSet handles the 'CreateTaskPushNotificationConfig' RPC method.
	// It configures push notifications for a specific task.
	OnPushNotificationSet(
		ctx context.Context,
		params protocol.TaskPushNotificationConfig,
	) (*protocol.TaskPushNotificationConfig, error)

	// OnPushNotificationGet handles the 'GetTaskPushNotificationConfig' RPC method.
	// It retrieves the current push notification configuration for a task.
	OnPushNotificationGet(
		ctx context.Context,
		params protocol.TaskIDParams,
	) (*protocol.TaskPushNotificationConfig, error)

	// OnPushNotificationList handles the v1.0 'ListTaskPushNotificationConfigs' RPC method.
	// It lists the push notification configurations registered for a task.
	OnPushNotificationList(
		ctx context.Context,
		params protocol.ListTaskPushNotificationConfigsParams,
	) (*protocol.ListTaskPushNotificationConfigsResult, error)

	// OnPushNotificationDelete handles the v1.0 'DeleteTaskPushNotificationConfig' RPC method.
	// It removes a push notification configuration from a task.
	OnPushNotificationDelete(
		ctx context.Context,
		params protocol.DeleteTaskPushNotificationConfigParams,
	) error

	// OnResubscribe handles a request corresponding to the 'tasks/resubscribe' RPC method.
	// It reestablishes an SSE stream for an existing non-terminal task. The
	// first event is the current Task snapshot.
	OnResubscribe(
		ctx context.Context,
		params protocol.TaskIDParams,
	) (<-chan protocol.StreamResponse, error)
}
