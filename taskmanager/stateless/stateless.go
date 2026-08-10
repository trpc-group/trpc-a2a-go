// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package stateless provides a request-bound TaskManager that retains no task,
// event, or conversation state after a request finishes.
package stateless

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const operationReturnImmediatelyTask = "SendMessage with returnImmediately for a Task response"

// TaskManager executes request-bound processor rounds without retaining tasks,
// events, or conversation history. A round may return either a direct Message
// or an execution-local Task. Tasks exist only for the originating request and
// cannot be retrieved, continued, canceled, or resubscribed to later.
type TaskManager struct {
	processor taskmanager.MessageProcessor

	executionMu sync.Mutex
	executions  map[uint64]context.CancelFunc
	nextID      uint64
	closed      bool
	closeOnce   sync.Once
}

var _ taskmanager.TaskManager = (*TaskManager)(nil)

// NewTaskManager creates a stateless TaskManager driven by processor.
func NewTaskManager(processor taskmanager.MessageProcessor) (*TaskManager, error) {
	if processor == nil {
		return nil, errors.New("processor cannot be nil")
	}
	return &TaskManager{
		processor:  processor,
		executions: make(map[uint64]context.CancelFunc),
	}, nil
}

// SupportsPushNotifications reports false because the manager retains no task
// or push configuration after the request.
func (*TaskManager) SupportsPushNotifications() bool { return false }

// OnSendMessage runs a request-bound processor round and returns its
// direct Message or its execution-local Task. returnImmediately has no effect
// on a direct Message response and is unsupported for a non-terminal Task,
// because no retained task exists for later observation.
func (m *TaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	events, ec, execCtx, cancel, err := m.startExecution(ctx, &request)
	if err != nil {
		return nil, err
	}
	defer cancel()

	round := newRound(ec)
	returnImmediately := request.Configuration != nil && !request.Configuration.IsBlocking()
	for {
		select {
		case <-execCtx.Done():
			cancel()
			go drain(events)
			return nil, execCtx.Err()
		case event, ok := <-events:
			if !ok {
				if err := execCtx.Err(); err != nil {
					return nil, err
				}
				if round.task != nil {
					if !round.task.Status.State.Terminal() {
						round.fail("processor finished without terminal state")
					}
					return protocol.NewSendMessageResponseTask(copyTask(round.task)), nil
				}
				return nil, taskmanager.ErrInvalidAgentResponse(
					"stateless TaskManager: processor produced no result",
				)
			}

			response, done, err := round.applyUnaryEvent(event, returnImmediately)
			if !done {
				continue
			}
			cancel()
			go drain(events)
			return response, err
		}
	}
}

// OnSendMessageStream runs a request-bound processor round. A direct response
// contains exactly one Message. A Task response starts with an execution-local
// Task snapshot followed by status and artifact updates until the round ends.
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	events, ec, execCtx, cancel, err := m.startExecution(ctx, &request)
	if err != nil {
		return nil, err
	}

	first, ok, err := receiveEvent(execCtx, events)
	if err != nil {
		cancel()
		go drain(events)
		return nil, err
	}
	if !ok {
		cancel()
		return nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor produced no result",
		)
	}

	if message, ok := first.(*protocol.Message); ok {
		message, err = normalizeDirectMessage(message, ec)
		if err != nil {
			cancel()
			go drain(events)
			return nil, err
		}
		cancel()
		go drain(events)
		out := make(chan protocol.StreamResponse, 1)
		out <- protocol.NewStreamResponseMessage(message)
		close(out)
		return out, nil
	}

	round := newRound(ec)
	initial := copyTask(round.ensureTask())
	firstResponse, done, err := round.applyTaskEvent(first)
	if err != nil {
		cancel()
		go drain(events)
		return nil, err
	}

	out := make(chan protocol.StreamResponse, 2)
	out <- protocol.NewStreamResponseTask(initial)
	out <- firstResponse
	if done {
		cancel()
		go drain(events)
		close(out)
		return out, nil
	}

	go consumeTaskStream(execCtx, events, cancel, round, out)
	return out, nil
}

// OnGetTask reports not-found because no Task survives its originating request.
func (*TaskManager) OnGetTask(
	_ context.Context,
	params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	return nil, taskmanager.ErrTaskNotFound(params.ID)
}

// OnCancelTask reports not-found because request-local executions have no
// externally addressable retained Task.
func (*TaskManager) OnCancelTask(
	_ context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	return nil, taskmanager.ErrTaskNotFound(params.ID)
}

// OnListTasks returns an empty page because the manager retains no Tasks.
func (*TaskManager) OnListTasks(
	_ context.Context,
	params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	return taskmanager.PaginateTasks(nil, params)
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

// OnResubscribe reports not-found because no Task survives its originating
// request.
func (*TaskManager) OnResubscribe(
	_ context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	return nil, taskmanager.ErrTaskNotFound(params.ID)
}

func (m *TaskManager) startExecution(
	ctx context.Context,
	request *protocol.SendMessageParams,
) (<-chan protocol.StreamEvent, *taskmanager.ExecContext, context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	ec, err := prepareExecContext(request)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	execCtx, cancel := context.WithCancel(ctx)
	release, err := m.registerExecution(cancel)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	events, err := m.processor.ProcessMessage(execCtx, ec)
	if err != nil {
		release()
		return nil, nil, nil, nil, err
	}
	if events == nil {
		release()
		return nil, nil, nil, nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor returned nil channel",
		)
	}
	return events, ec, execCtx, release, nil
}

func (m *TaskManager) registerExecution(
	cancel context.CancelFunc,
) (context.CancelFunc, error) {
	m.executionMu.Lock()
	if m.closed {
		m.executionMu.Unlock()
		return nil, taskmanager.ErrInternalError("task manager is closed")
	}
	m.nextID++
	id := m.nextID
	m.executions[id] = cancel
	m.executionMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			m.executionMu.Lock()
			delete(m.executions, id)
			m.executionMu.Unlock()
		})
	}, nil
}

// Close refuses new requests and cancels every live request-bound execution.
// It is safe to call Close multiple times.
func (m *TaskManager) Close() error {
	m.closeOnce.Do(func() {
		m.executionMu.Lock()
		m.closed = true
		cancels := make([]context.CancelFunc, 0, len(m.executions))
		for _, cancel := range m.executions {
			cancels = append(cancels, cancel)
		}
		m.executionMu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
	})
	return nil
}

func prepareExecContext(
	request *protocol.SendMessageParams,
) (*taskmanager.ExecContext, error) {
	if request.Message.TaskID != nil && *request.Message.TaskID != "" {
		return nil, taskmanager.ErrTaskNotFound(*request.Message.TaskID)
	}
	if request.Configuration != nil && request.Configuration.PushConfig != nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
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

type round struct {
	ec   *taskmanager.ExecContext
	task *protocol.Task
}

func newRound(ec *taskmanager.ExecContext) *round { return &round{ec: ec} }

func (r *round) ensureTask() *protocol.Task {
	if r.task == nil {
		r.task = protocol.NewTask(r.ec.TaskID, r.ec.ContextID)
	}
	return r.task
}

func (r *round) applyUnaryEvent(
	event protocol.StreamEvent,
	returnImmediately bool,
) (*protocol.SendMessageResponse, bool, error) {
	switch typed := event.(type) {
	case *protocol.Message:
		if r.task == nil {
			message, err := normalizeDirectMessage(typed, r.ec)
			if err != nil {
				return nil, true, err
			}
			return protocol.NewSendMessageResponseMessage(message), true, nil
		}
		if _, _, err := r.applyTaskEvent(typed); err != nil {
			return r.failedTaskResponse(err.Error()), true, nil
		}
		return nil, false, nil

	case *protocol.TaskStatusUpdateEvent, *protocol.TaskArtifactUpdateEvent:
		_, terminal, err := r.applyTaskEvent(event)
		if err != nil {
			if r.task != nil {
				return r.failedTaskResponse(err.Error()), true, nil
			}
			return nil, true, err
		}
		if returnImmediately && !terminal {
			return nil, true, taskmanager.ErrUnsupportedOperation(operationReturnImmediatelyTask)
		}
		if terminal {
			return protocol.NewSendMessageResponseTask(copyTask(r.task)), true, nil
		}
		return nil, false, nil

	case *protocol.Task:
		if r.task != nil {
			return r.failedTaskResponse("processor emitted forbidden Task snapshot"), true, nil
		}
		return nil, true, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted forbidden Task snapshot",
		)

	default:
		reason := fmt.Sprintf("unsupported processor event %T", event)
		if r.task != nil {
			return r.failedTaskResponse(reason), true, nil
		}
		return nil, true, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: " + reason,
		)
	}
}

func (r *round) failedTaskResponse(reason string) *protocol.SendMessageResponse {
	r.fail(reason)
	return protocol.NewSendMessageResponseTask(copyTask(r.task))
}

func (r *round) applyTaskEvent(
	event protocol.StreamEvent,
) (protocol.StreamResponse, bool, error) {
	switch typed := event.(type) {
	case *protocol.TaskStatusUpdateEvent:
		if typed == nil {
			return protocol.StreamResponse{}, false, taskmanager.ErrInvalidAgentResponse(
				"stateless TaskManager: processor emitted nil status event",
			)
		}
		if err := r.stampTaskEventIDs(&typed.TaskID, &typed.ContextID); err != nil {
			return protocol.StreamResponse{}, false, err
		}
		if typed.Status.State == "" || typed.Status.State == protocol.TaskStateUnspecified {
			return protocol.StreamResponse{}, false, taskmanager.ErrInvalidAgentResponse(
				"stateless TaskManager: processor emitted status without a state",
			)
		}
		if isSuspended(typed.Status.State) {
			return protocol.StreamResponse{}, false, taskmanager.ErrUnsupportedOperation(
				"suspended Task in stateless TaskManager",
			)
		}
		if typed.Status.Message != nil {
			if err := normalizeTaskMessage(typed.Status.Message, r.ec); err != nil {
				return protocol.StreamResponse{}, false, err
			}
		}
		if typed.Status.Timestamp == "" {
			typed.Status.Timestamp = nowTimestamp()
		}
		typed.Final = typed.Status.State.Terminal()
		r.ensureTask().Status = typed.Status
		return protocol.NewStreamResponseStatusUpdate(typed), typed.Status.State.Terminal(), nil

	case *protocol.TaskArtifactUpdateEvent:
		if typed == nil {
			return protocol.StreamResponse{}, false, taskmanager.ErrInvalidAgentResponse(
				"stateless TaskManager: processor emitted nil artifact event",
			)
		}
		if err := r.stampTaskEventIDs(&typed.TaskID, &typed.ContextID); err != nil {
			return protocol.StreamResponse{}, false, err
		}
		if typed.Artifact.ArtifactID == "" {
			return protocol.StreamResponse{}, false, taskmanager.ErrInvalidAgentResponse(
				"stateless TaskManager: processor emitted artifact without an ID",
			)
		}
		appendChunk := typed.Append != nil && *typed.Append
		task := r.ensureTask()
		task.Artifacts, _ = protocol.AppendArtifact(task.Artifacts, typed.Artifact, appendChunk)
		return protocol.NewStreamResponseArtifactUpdate(typed), false, nil

	case *protocol.Message:
		if err := normalizeTaskMessage(typed, r.ec); err != nil {
			return protocol.StreamResponse{}, false, err
		}
		return protocol.NewStreamResponseMessage(typed), false, nil
	case *protocol.Task:
		return protocol.StreamResponse{}, false, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted forbidden Task snapshot",
		)
	default:
		return protocol.StreamResponse{}, false, taskmanager.ErrInvalidAgentResponse(fmt.Sprintf(
			"stateless TaskManager: unsupported processor event %T", event,
		))
	}
}

func (r *round) stampTaskEventIDs(taskID, contextID *string) error {
	if *taskID == "" {
		*taskID = r.ec.TaskID
	} else if *taskID != r.ec.TaskID {
		return taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted event for a foreign task",
		)
	}
	if *contextID == "" {
		*contextID = r.ec.ContextID
	} else if *contextID != r.ec.ContextID {
		return taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted event for a foreign context",
		)
	}
	return nil
}

func (r *round) fail(reason string) *protocol.TaskStatusUpdateEvent {
	message := protocol.NewAgentText(reason)
	_ = normalizeTaskMessage(message, r.ec)
	event := &protocol.TaskStatusUpdateEvent{
		TaskID:    r.ec.TaskID,
		ContextID: r.ec.ContextID,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateFailed,
			Message:   message,
			Timestamp: nowTimestamp(),
		},
		Final: true,
	}
	r.ensureTask().Status = event.Status
	return event
}

func consumeTaskStream(
	ctx context.Context,
	events <-chan protocol.StreamEvent,
	cancel context.CancelFunc,
	round *round,
	out chan protocol.StreamResponse,
) {
	defer cancel()
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			go drain(events)
			return
		case event, ok := <-events:
			if !ok {
				if !round.task.Status.State.Terminal() {
					sendResponse(ctx, out, protocol.NewStreamResponseStatusUpdate(
						round.fail("processor finished without terminal state"),
					))
				}
				return
			}
			response, done, err := round.applyTaskEvent(event)
			if err != nil {
				log.Errorf("stateless TaskManager: task contract violation: %v", err)
				sendResponse(ctx, out, protocol.NewStreamResponseStatusUpdate(round.fail(err.Error())))
				cancel()
				go drain(events)
				return
			}
			if !sendResponse(ctx, out, response) {
				go drain(events)
				return
			}
			if done {
				cancel()
				go drain(events)
				return
			}
		}
	}
}

func normalizeDirectMessage(
	message *protocol.Message,
	ec *taskmanager.ExecContext,
) (*protocol.Message, error) {
	if message == nil {
		return nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted nil Message",
		)
	}
	if message.TaskID != nil && *message.TaskID != "" && *message.TaskID != ec.TaskID {
		return nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted Message for a foreign task",
		)
	}
	if message.ContextID != nil && *message.ContextID != "" && *message.ContextID != ec.ContextID {
		return nil, taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted Message for a foreign context",
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

func normalizeTaskMessage(message *protocol.Message, ec *taskmanager.ExecContext) error {
	if message == nil {
		return taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted nil task Message",
		)
	}
	if message.TaskID != nil && *message.TaskID != "" && *message.TaskID != ec.TaskID {
		return taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted status Message for a foreign task",
		)
	}
	if message.ContextID != nil && *message.ContextID != "" && *message.ContextID != ec.ContextID {
		return taskmanager.ErrInvalidAgentResponse(
			"stateless TaskManager: processor emitted status Message for a foreign context",
		)
	}
	taskID := ec.TaskID
	contextID := ec.ContextID
	message.TaskID = &taskID
	message.ContextID = &contextID
	message.Role = protocol.MessageRoleAgent
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}
	return nil
}

func receiveEvent(
	ctx context.Context,
	events <-chan protocol.StreamEvent,
) (protocol.StreamEvent, bool, error) {
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case event, ok := <-events:
		return event, ok, nil
	}
}

func sendResponse(
	ctx context.Context,
	out chan<- protocol.StreamResponse,
	response protocol.StreamResponse,
) bool {
	select {
	case out <- response:
		return true
	case <-ctx.Done():
		return false
	}
}

func copyTask(task *protocol.Task) *protocol.Task {
	snapshot := *task
	snapshot.Artifacts = append([]protocol.Artifact(nil), task.Artifacts...)
	snapshot.History = nil
	return &snapshot
}

func isSuspended(state protocol.TaskState) bool {
	return state == protocol.TaskStateInputRequired || state == protocol.TaskStateAuthRequired
}

func nowTimestamp() string { return time.Now().UTC().Format(time.RFC3339) }

func drain(events <-chan protocol.StreamEvent) {
	for range events {
	}
}
