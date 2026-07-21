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
	// TaskID is pre-allocated by the framework. In a task-capable manager, whether
	// a task actually comes into existence depends on the MessageProcessor: the
	// framework creates (and persists) the task lazily when the first task event
	// is emitted. On a continuation (see Task) it is the existing task's ID. A
	// Message-only manager may use the ID only to identify the execution round.
	TaskID string

	// Task is non-nil on a continuation: when a client follows up on a task in
	// the input-required or auth-required state by sending another message
	// with the same taskId, Task carries the current task snapshot. It is nil
	// on the first round.
	Task *protocol.Task

	// Message is the incoming message to process.
	Message protocol.Message

	// Streaming reports which RPC shape started this round. It is true for
	// SendStreamingMessage and false for SendMessage, including a SendMessage
	// configured with returnImmediately. Processors can use it when their live
	// stream emits deltas but their unary response must end with a complete
	// Message.
	Streaming bool

	// ContextID is the conversation context ID. When the request does not
	// carry one, the framework generates it before invoking the MessageProcessor.
	ContextID string

	// Tenant is the A2A v1.0 tenant the request is addressed to (from the
	// request body). It lets one process host multiple agents: the MessageProcessor
	// dispatches on it. Empty for single-agent deployments.
	Tenant string

	// History is a snapshot of the conversation history for ContextID,
	// truncated per the manager's configuration. It is nil for managers that do
	// not retain conversation history. (The client-requested historyLength
	// applies to the task returned in responses, not to this snapshot.)
	History []protocol.Message

	// AcceptedOutputModes is the client's declared list of accepted output
	// modes, when provided.
	AcceptedOutputModes []string

	// PushConfig is the push-notification configuration carried inline on the
	// send request (configuration.pushNotificationConfig), when provided. It is
	// an informational copy: the manager has already registered it for the task
	// (or rejected the request with PushNotificationNotSupported when push is
	// not configured), exactly as an explicit tasks/pushNotificationConfig/set
	// would. A MessageProcessor delivering manually (push.Config.ManualDelivery)
	// can use it as the webhook to push to; configs registered via the RPC live
	// in the manager's store and are not surfaced here.
	PushConfig *protocol.TaskPushNotificationConfig
}

// MessageProcessor is the single interface implemented by users to define an agent's
// behavior: process one message and report progress by sending events on the
// returned channel.
//
// Accepted event types depend on the TaskManager:
//   - *protocol.Message is a direct reply accepted by every manager. No task
//     comes into existence for a pure-message exchange.
//   - *protocol.TaskStatusUpdateEvent and *protocol.TaskArtifactUpdateEvent
//     drive the task identified by ExecContext.TaskID in task-capable managers.
//     The manager creates the task on the first task event, persists every event
//     before broadcasting it to subscribers, and derives the unary result from
//     the stream. Message-only managers reject these events.
//   - *protocol.Task is never accepted: task snapshots are materialized by
//     task-capable managers from the event stream. Emitting one is a contract
//     violation.
//
// Task events accepted by a task-capable manager may leave TaskID/ContextID
// empty; the manager stamps them. A direct Message may leave ContextID empty.
// Foreign IDs are a contract violation: task-capable managers mark an existing
// task failed, while request-bound Message-only managers cancel the round. In
// both cases the remaining events are discarded and the channel is drained.
//
// Sending an event hands it off to the framework: the processor must not
// retain and mutate an event (or its Parts/Message) after sending it.
//
// For a task-capable manager, closing the channel ends the round:
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
// A request-bound Message-only manager defines completion in terms of direct
// replies and channel closure; it does not apply task close rules.
//
// In a task-capable manager, a terminal or suspend-state status event ends the
// round's writes early: a suspend event yields the task — the framework
// immediately admits a continuation, so this round no longer owns the task and
// any events it emits afterwards are discarded (close the channel after
// suspending). The message/stream response stream ends at the terminal or
// suspend frame; the channel itself is still drained until closed.
//
// For task-capable managers, ctx is canceled when the task is canceled via
// CancelTask. A client disconnect does NOT cancel ctx: the work keeps running
// and its results remain retrievable (GetTask / SubscribeToTask). A manager
// that explicitly provides request-bound, Message-only execution may cancel
// ctx on disconnect because it has no task to retrieve or resubscribe to. The
// framework always drains the channel until it is closed, so senders never
// leak.
//
// The framework starts consuming the channel only after ProcessMessage
// returns: sends beyond the channel buffer from inside ProcessMessage itself
// block forever. Emit from a goroutine, or use TaskHandle (NewTaskHandle) for
// a synchronous body — its emits before Events() never block.
//
// Returning a non-nil error means the round failed to start: no events are
// consumed and the error is mapped to a JSON-RPC error. To report a business
// failure, use an event supported by the selected manager and close the
// channel: a TASK_STATE_FAILED status for a task-capable manager, or a direct
// agent Message for a Message-only manager.
type MessageProcessor interface {
	ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}

// TaskManager defines the interface for serving A2A message and task methods.
// Task-capable implementations handle task creation, updates, retrieval,
// cancellation, and events, delegating the agent logic to an injected
// MessageProcessor. Message-only implementations may reject task methods with
// ErrUnsupportedOperation.
//
// This interface corresponds to the Task Service defined in the A2A Specification.
type TaskManager interface {
	// SupportsPushNotifications reports whether push-config registration and
	// delivery are available. It deliberately exposes capability rather than a
	// concrete transport so queue- and outbox-backed implementations fit too.
	SupportsPushNotifications() bool

	// OnSendMessage handles a request corresponding to the 'message/send' RPC method.
	// A task-capable manager derives the final task snapshot when task events were
	// emitted, otherwise the last message. With returnImmediately=true it returns
	// as soon as the first immediate result is persisted, while execution continues
	// in the background. A request-bound Message-only manager returns its last
	// message and may reject returnImmediately because it cannot continue
	// execution after the request completes.
	OnSendMessage(
		ctx context.Context,
		request protocol.SendMessageParams,
	) (*protocol.SendMessageResponse, error)

	// OnSendMessageStream handles a request corresponding to the 'message/stream' RPC method.
	// It invokes the MessageProcessor and returns a channel that carries emitted
	// events. Task-capable managers persist task events before delivery; a
	// Message-only manager may forward direct replies without persistence. The
	// channel is closed when the round ends; setup errors are returned directly.
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
		params protocol.GetTaskPushNotificationConfigParams,
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
