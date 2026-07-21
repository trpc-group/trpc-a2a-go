// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package stateless provides a request-bound TaskManager for agents that only
// exchange direct Messages and do not expose A2A task lifecycle operations.
package stateless

import (
	"context"
	"errors"
	"fmt"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const (
	operationTaskContinuation  = "task continuation"
	operationReturnImmediately = "SendMessage with returnImmediately"
)

// TaskManager executes request-bound, Message-only processor rounds without
// retaining tasks or conversation history.
//
// It is intended for adapters that keep their own conversation context and do
// not need GetTask, ListTasks, CancelTask, SubscribeToTask, push notifications,
// or resumable background execution. Use taskmanager/memory or
// taskmanager/redis when any of those capabilities are required.
type TaskManager struct {
	processor taskmanager.MessageProcessor
}

var _ taskmanager.TaskManager = (*TaskManager)(nil)

// NewTaskManager creates a stateless TaskManager driven by processor.
func NewTaskManager(processor taskmanager.MessageProcessor) (*TaskManager, error) {
	if processor == nil {
		return nil, errors.New("processor cannot be nil")
	}
	return &TaskManager{processor: processor}, nil
}

// SupportsPushNotifications reports false because stateless managers retain no
// task or push configuration.
func (*TaskManager) SupportsPushNotifications() bool { return false }

// OnSendMessage runs a request-bound processor round and returns its last
// Message. Task events, task continuations, and returnImmediately are rejected.
func (m *TaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	events, ec, cancel, err := m.startExecution(ctx, &request, true)
	if err != nil {
		return nil, err
	}
	defer cancel()

	var last *protocol.Message
	for {
		select {
		case <-ctx.Done():
			go drain(events)
			return nil, ctx.Err()
		case event, ok := <-events:
			if !ok {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if last == nil {
					return nil, taskmanager.ErrInvalidAgentResponse(
						"stateless TaskManager: processor produced no Message",
					)
				}
				return protocol.NewSendMessageResponseMessage(last), nil
			}
			message, err := normalizeMessage(event, ec)
			if err != nil {
				cancel()
				go drain(events)
				return nil, err
			}
			last = message
		}
	}
}

// OnSendMessageStream runs a request-bound processor round and forwards each
// Message until the processor closes its channel or the request context is
// canceled. If the processor emits a task event, the processor context is
// canceled and the stream is closed.
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	events, ec, cancel, err := m.startExecution(ctx, &request, false)
	if err != nil {
		return nil, err
	}

	out := make(chan protocol.StreamResponse)
	go func() {
		defer cancel()
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				go drain(events)
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				message, err := normalizeMessage(event, ec)
				if err != nil {
					log.Errorf("stateless TaskManager: closing stream: %v", err)
					cancel()
					go drain(events)
					return
				}
				select {
				case out <- protocol.NewStreamResponseMessage(message):
				case <-ctx.Done():
					go drain(events)
					return
				}
			}
		}
	}()
	return out, nil
}

// OnGetTask rejects task lookup because stateless managers retain no tasks.
func (*TaskManager) OnGetTask(
	context.Context,
	protocol.TaskQueryParams,
) (*protocol.Task, error) {
	return nil, taskmanager.ErrUnsupportedOperation(protocol.MethodTasksGet)
}

// OnCancelTask rejects cancellation because stateless executions are canceled
// through their request context and have no task identity.
func (*TaskManager) OnCancelTask(
	context.Context,
	protocol.TaskIDParams,
) (*protocol.Task, error) {
	return nil, taskmanager.ErrUnsupportedOperation(protocol.MethodTasksCancel)
}

// OnListTasks rejects task listing because stateless managers retain no tasks.
func (*TaskManager) OnListTasks(
	context.Context,
	protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	return nil, taskmanager.ErrUnsupportedOperation(protocol.MethodTasksList)
}

// OnPushNotificationSet rejects push registration.
func (*TaskManager) OnPushNotificationSet(
	context.Context,
	protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	return nil, taskmanager.ErrPushNotificationNotSupported()
}

// OnPushNotificationGet rejects push lookup.
func (*TaskManager) OnPushNotificationGet(
	context.Context,
	protocol.GetTaskPushNotificationConfigParams,
) (*protocol.TaskPushNotificationConfig, error) {
	return nil, taskmanager.ErrPushNotificationNotSupported()
}

// OnPushNotificationList rejects push listing.
func (*TaskManager) OnPushNotificationList(
	context.Context,
	protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	return nil, taskmanager.ErrPushNotificationNotSupported()
}

// OnPushNotificationDelete rejects push deletion.
func (*TaskManager) OnPushNotificationDelete(
	context.Context,
	protocol.DeleteTaskPushNotificationConfigParams,
) error {
	return taskmanager.ErrPushNotificationNotSupported()
}

// OnResubscribe rejects subscription because stateless streams are bound to
// their originating request and cannot be resumed.
func (*TaskManager) OnResubscribe(
	context.Context,
	protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	return nil, taskmanager.ErrUnsupportedOperation(protocol.MethodTasksResubscribe)
}

func (m *TaskManager) startExecution(
	ctx context.Context,
	request *protocol.SendMessageParams,
	rejectReturnImmediately bool,
) (<-chan protocol.StreamEvent, *taskmanager.ExecContext, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	ec, err := prepareExecContext(request, rejectReturnImmediately)
	if err != nil {
		return nil, nil, nil, err
	}

	execCtx, cancel := context.WithCancel(ctx)
	events, err := m.processor.ProcessMessage(execCtx, ec)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	if events == nil {
		cancel()
		return nil, nil, nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor returned nil channel",
		)
	}
	return events, ec, cancel, nil
}

func prepareExecContext(
	request *protocol.SendMessageParams,
	rejectReturnImmediately bool,
) (*taskmanager.ExecContext, error) {
	if request.Message.TaskID != nil && *request.Message.TaskID != "" {
		return nil, taskmanager.ErrUnsupportedOperation(operationTaskContinuation)
	}
	if request.Configuration != nil {
		if request.Configuration.PushConfig != nil {
			return nil, taskmanager.ErrPushNotificationNotSupported()
		}
		if rejectReturnImmediately && !request.Configuration.IsBlocking() {
			return nil, taskmanager.ErrUnsupportedOperation(operationReturnImmediately)
		}
	}

	message := request.Message
	message.TaskID = nil
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}
	contextID := ""
	if message.ContextID != nil {
		contextID = *message.ContextID
	}
	if contextID == "" {
		contextID = protocol.GenerateContextID()
	}
	message.ContextID = &contextID

	var acceptedOutputModes []string
	if request.Configuration != nil {
		acceptedOutputModes = append(
			[]string(nil),
			request.Configuration.AcceptedOutputModes...,
		)
	}
	return &taskmanager.ExecContext{
		TaskID:              protocol.GenerateTaskID(),
		Message:             message,
		ContextID:           contextID,
		Tenant:              request.Tenant,
		AcceptedOutputModes: acceptedOutputModes,
	}, nil
}

func normalizeMessage(
	event protocol.StreamEvent,
	ec *taskmanager.ExecContext,
) (*protocol.Message, error) {
	message, ok := event.(*protocol.Message)
	if !ok || message == nil {
		return nil, taskmanager.ErrInvalidAgentResponse(fmt.Sprintf(
			"stateless TaskManager accepts only Message events, got %T",
			event,
		))
	}
	if message.TaskID != nil && *message.TaskID != "" {
		return nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager does not accept task-bound Message events",
		)
	}
	if message.ContextID != nil && *message.ContextID != "" &&
		*message.ContextID != ec.ContextID {
		return nil, taskmanager.ErrInvalidAgentResponse(
			"processor emitted Message for a foreign context",
		)
	}

	contextID := ec.ContextID
	message.TaskID = nil
	message.ContextID = &contextID
	message.Role = protocol.MessageRoleAgent
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}
	return message, nil
}

func drain(events <-chan protocol.StreamEvent) {
	for range events {
	}
}
