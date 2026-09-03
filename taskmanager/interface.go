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

// OwnerResolver derives the application-defined owner scope for retained task
// data from the authenticated request context. The owner may identify a user,
// group, project, or another authorization boundary. Retaining task managers
// use it together with the request tenant; it is never written into protocol
// objects.
type OwnerResolver func(context.Context) (string, error)

// ExecContext is the read-only snapshot of one incoming message for a
// MessageProcessor to process.
type ExecContext struct {
	// TaskID is pre-allocated by the framework. Whether a task actually comes
	// into existence depends on the MessageProcessor: the manager materializes it
	// lazily when the first task event is emitted. A retaining manager persists
	// it; a stateless manager keeps it only for the originating request. On a
	// continuation (see Task) it is the existing retained task's ID.
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
// Accepted event types:
//   - *protocol.Message is a direct reply. No task comes into existence for a
//     pure-message exchange.
//   - *protocol.TaskStatusUpdateEvent and *protocol.TaskArtifactUpdateEvent
//     drive the task identified by ExecContext.TaskID. The manager creates the
//     task on the first task event and derives the unary result from the stream.
//     Retaining managers persist events before broadcasting them; a stateless
//     manager applies them only to the request-local task snapshot.
//   - *protocol.Task is never accepted: task snapshots are materialized by
//     managers from the event stream. Emitting one is a contract violation.
//
// The first valid event selects the response shape without emitting a Task: a
// Message completes a taskless direct response, and later events from that round
// are discarded; a status or artifact selects a task lifecycle. For
// message/stream, the manager emits an operation-local Task snapshot before that
// round's first task update. A fresh round starts from SUBMITTED; a continuation
// starts from ExecContext.Task. This framing snapshot is not a processor event
// and is not journaled or broadcast as an update. Retaining managers persist the
// triggering task event before delivering the response frames.
//
// Task events may leave TaskID/ContextID empty; the manager stamps them. A
// direct Message may leave ContextID empty. Foreign IDs are a contract
// violation: an already-materialized task is failed, otherwise the request is
// rejected. The remaining events are discarded and the channel is drained.
//
// Sending an event hands it off to the framework: the processor must not
// retain and mutate an event (or its Parts/Message) after sending it.
//
// Closing the channel ends the round:
//   - with the task in a terminal state, or after a pure-message reply: a
//     normal end;
//   - in input-required / auth-required: a retaining manager keeps the task
//     suspended awaiting a follow-up message; a request-local manager may reject
//     suspension because it cannot accept a continuation;
//   - in submitted / working: the framework marks the task failed —
//     finishing without a conclusion is a MessageProcessor bug;
//   - after a CancelTask-triggered ctx cancellation: closing without a
//     terminal state leads the framework to mark the task CANCELED on the
//     processor's behalf.
//
// In a retaining manager, a terminal or suspend-state status event ends the
// round's writes early: a suspend event yields the task — the manager
// immediately admits a continuation, so this round no longer owns the task and
// any events it emits afterwards are discarded (close the channel after
// suspending). The message/stream response stream ends at the terminal or
// suspend frame; the channel itself is still drained until closed.
//
// For retaining managers, CancelTask and manager shutdown cancel active work.
// A retaining manager may also cancel a round's ctx after accepting a terminal
// status frame. The Memory and Redis managers cancel a yielded round's ctx
// after publishing its input-required/auth-required frame and before releasing
// the task slot. These are round teardown, not task cancellation: the stored
// Task keeps its accepted terminal or suspended state and the processor must
// close its channel promptly. A client disconnect does NOT cancel retaining
// work, whose results remain retrievable (GetTask / SubscribeToTask). A
// stateless manager cancels ctx on disconnect and discards its request-local
// task. The framework always drains the channel until it is closed, so senders
// never leak.
//
// The framework starts consuming the channel only after ProcessMessage
// returns: sends beyond the channel buffer from inside ProcessMessage itself
// block forever. Emit from a goroutine, or use TaskHandle (NewTaskHandle) for
// a synchronous body — its emits before Events() never block.
//
// Returning a non-nil error means the round failed to start: no events are
// consumed and the error is mapped to the active protocol binding's error
// representation. To report a business failure, emit a TASK_STATE_FAILED status
// or a direct agent Message and close the channel.
type MessageProcessor interface {
	ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}

// TaskManager defines the interface for serving A2A message and task methods.
// Implementations may retain tasks across requests or keep them only for the
// originating request. Both derive Message or Task results from an injected
// MessageProcessor; a request-local implementation reports not-found for task
// methods after the originating request ends.
//
// This interface corresponds to the Task Service defined in the A2A Specification.
type TaskManager interface {
	// SupportsPushNotifications reports whether push-config registration and
	// delivery are available. It deliberately exposes capability rather than a
	// concrete transport so queue- and outbox-backed implementations fit too.
	SupportsPushNotifications() bool

	// OnSendMessage handles a request corresponding to the 'message/send' RPC method.
	// The manager derives a task snapshot when task events were emitted, otherwise
	// a direct Message. A retaining manager can honor returnImmediately=true by
	// continuing in the background. A request-local manager may reject it for a
	// non-terminal Task because no retained result exists for later observation.
	OnSendMessage(
		ctx context.Context,
		request protocol.SendMessageParams,
	) (*protocol.SendMessageResponse, error)

	// OnSendMessageStream handles a request corresponding to the 'message/stream' RPC method.
	// It invokes the MessageProcessor and returns a channel carrying the derived
	// response stream. A pure Message round has no Task framing. A task-producing
	// round starts with a manager-materialized Task snapshot, followed by the
	// processor's status/artifact events. Retaining managers persist task events
	// before delivery; stateless managers apply them only to the request-local
	// task. The channel is closed when the round ends; setup errors are returned
	// directly.
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
