// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package stateless

import (
	"context"
	"errors"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

type processorFunc func(
	context.Context,
	*taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error)

func intPtr(i int) *int { return &i }

func (f processorFunc) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	return f(ctx, ec)
}

func eventChannel(events ...protocol.StreamEvent) <-chan protocol.StreamEvent {
	out := make(chan protocol.StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}

func messageParams(text string) protocol.SendMessageParams {
	return protocol.SendMessageParams{
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []*protocol.Part{protocol.NewTextPart(text)},
		},
	}
}

func statusEvent(state protocol.TaskState, text string) *protocol.TaskStatusUpdateEvent {
	event := &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: state}}
	if text != "" {
		event.Status.Message = protocol.NewAgentText(text)
	}
	return event
}

func artifactEvent(id, text string, appendChunk bool) *protocol.TaskArtifactUpdateEvent {
	return &protocol.TaskArtifactUpdateEvent{
		Artifact: protocol.Artifact{
			ArtifactID: id,
			Parts:      []*protocol.Part{protocol.NewTextPart(text)},
		},
		Append: &appendChunk,
	}
}

func messageText(message *protocol.Message) string {
	if message == nil {
		return ""
	}
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

func artifactText(artifact protocol.Artifact) string {
	var text string
	for _, part := range artifact.Parts {
		text += part.TextContent()
	}
	return text
}

func newManager(t *testing.T, processor taskmanager.MessageProcessor) *TaskManager {
	t.Helper()
	manager, err := NewTaskManager(processor)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	return manager
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func collectStream(t *testing.T, stream <-chan protocol.StreamResponse) []protocol.StreamResponse {
	t.Helper()
	var responses []protocol.StreamResponse
	deadline := time.After(2 * time.Second)
	for {
		select {
		case response, ok := <-stream:
			if !ok {
				return responses
			}
			responses = append(responses, response)
		case <-deadline:
			t.Fatal("timed out draining stream")
		}
	}
}

func TestNewTaskManager(t *testing.T) {
	if _, err := NewTaskManager(nil); err == nil {
		t.Fatal("NewTaskManager(nil) succeeded, want error")
	}

	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(protocol.NewAgentText("ok")), nil
	}))
	if manager.SupportsPushNotifications() {
		t.Fatal("SupportsPushNotifications returned true")
	}
}

func TestCloseCancelsExecutionsAndRejectsNewRequests(t *testing.T) {
	processorStarted := make(chan struct{})
	processorCanceled := make(chan struct{})
	manager := newManager(t, processorFunc(func(
		ctx context.Context,
		_ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		close(processorStarted)
		out := make(chan protocol.StreamEvent)
		go func() {
			<-ctx.Done()
			close(processorCanceled)
			close(out)
		}()
		return out, nil
	}))

	result := make(chan error, 1)
	go func() {
		_, err := manager.OnSendMessage(context.Background(), messageParams("hello"))
		result <- err
	}()
	waitSignal(t, processorStarted, "processor start")
	if err := manager.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	waitSignal(t, processorCanceled, "processor cancellation")
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active request error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for active request")
	}

	_, err := manager.OnSendMessage(context.Background(), messageParams("again"))
	if err == nil {
		t.Fatal("request after Close succeeded")
	}
}

func TestOnSendMessage_DirectMessage(t *testing.T) {
	processorCanceled := make(chan struct{})
	producerDone := make(chan struct{})
	var captured *taskmanager.ExecContext
	manager := newManager(t, processorFunc(func(
		ctx context.Context,
		ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		captured = ec
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			defer close(producerDone)
			out <- &protocol.Message{
				Role:  protocol.MessageRoleUser,
				Parts: []*protocol.Part{protocol.NewTextPart("reply")},
			}
			<-ctx.Done()
			close(processorCanceled)
			out <- protocol.NewAgentText("discarded")
		}()
		return out, nil
	}))

	request := messageParams("hello")
	request.Tenant = "tenant-a"
	request.Configuration = &protocol.SendMessageConfiguration{
		AcceptedOutputModes: []string{"text/plain", "application/json"},
	}
	response, err := manager.OnSendMessage(context.Background(), request)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}

	message := response.GetMessage()
	if got := messageText(message); got != "reply" {
		t.Fatalf("message text = %q, want reply", got)
	}
	if message.Role != protocol.MessageRoleAgent {
		t.Errorf("message role = %s, want agent", message.Role)
	}
	if message.MessageID == "" {
		t.Error("message ID was not generated")
	}
	if message.TaskID != nil {
		t.Errorf("message task ID = %q, want nil", *message.TaskID)
	}
	if message.ContextID == nil || *message.ContextID == "" {
		t.Fatal("message context ID was not generated")
	}

	if captured == nil {
		t.Fatal("processor did not receive ExecContext")
	}
	if captured.TaskID == "" || captured.Task != nil || captured.History != nil {
		t.Errorf("unexpected ExecContext task fields: %+v", captured)
	}
	if captured.Tenant != request.Tenant {
		t.Errorf("ExecContext tenant = %q, want %q", captured.Tenant, request.Tenant)
	}
	if captured.Message.MessageID == "" || captured.Message.ContextID == nil {
		t.Errorf("input Message was not normalized: %+v", captured.Message)
	}
	if len(captured.AcceptedOutputModes) != 2 {
		t.Errorf("accepted output modes = %#v", captured.AcceptedOutputModes)
	}
	waitSignal(t, processorCanceled, "processor cancellation")
	waitSignal(t, producerDone, "producer drain")
}

func TestOnSendMessage_DirectMessageAllowsReturnImmediately(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(protocol.NewAgentText("reply")), nil
	}))
	returnImmediately := true
	request := messageParams("hello")
	request.Configuration = &protocol.SendMessageConfiguration{
		ReturnImmediately: &returnImmediately,
	}
	response, err := manager.OnSendMessage(context.Background(), request)
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	if got := messageText(response.GetMessage()); got != "reply" {
		t.Fatalf("message text = %q, want reply", got)
	}
}

func TestOnSendMessage_RequestLocalTask(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(
			statusEvent(protocol.TaskStateWorking, "working"),
			artifactEvent("artifact-1", "hello ", false),
			artifactEvent("artifact-1", "world", true),
			statusEvent(protocol.TaskStateCompleted, "done"),
		), nil
	}))

	response, err := manager.OnSendMessage(context.Background(), messageParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		t.Fatalf("result = %#v, want Task", response.Result)
	}
	if task.ID == "" || task.ContextID == "" {
		t.Errorf("task IDs were not generated: %+v", task)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Errorf("task state = %s, want completed", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.TaskID == nil ||
		*task.Status.Message.TaskID != task.ID {
		t.Errorf("status Message was not stamped: %+v", task.Status.Message)
	}
	if len(task.Artifacts) != 1 || artifactText(task.Artifacts[0]) != "hello world" {
		t.Errorf("task artifacts = %+v", task.Artifacts)
	}
	if task.History != nil {
		t.Errorf("task history = %+v, want nil", task.History)
	}

	_, err = manager.OnGetTask(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("OnGetTask error = %v, want task not found", err)
	}
}

func TestOnSendMessage_NonTerminalCloseFailsRequestLocalTask(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(statusEvent(protocol.TaskStateWorking, "working")), nil
	}))

	response, err := manager.OnSendMessage(context.Background(), messageParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessage failed: %v", err)
	}
	task := response.GetTask()
	if task == nil || task.Status.State != protocol.TaskStateFailed {
		t.Fatalf("result = %#v, want failed Task", response.Result)
	}
}

func TestOnSendMessage_ReturnImmediatelyTask(t *testing.T) {
	returnImmediately := true
	request := messageParams("run")
	request.Configuration = &protocol.SendMessageConfiguration{
		ReturnImmediately: &returnImmediately,
	}

	t.Run("non-terminal task is unsupported", func(t *testing.T) {
		manager := newManager(t, processorFunc(func(
			context.Context,
			*taskmanager.ExecContext,
		) (<-chan protocol.StreamEvent, error) {
			return eventChannel(statusEvent(protocol.TaskStateWorking, "working")), nil
		}))
		_, err := manager.OnSendMessage(context.Background(), request)
		if !errors.Is(err, taskmanager.ErrUnsupportedOperationSentinel) {
			t.Fatalf("error = %v, want unsupported operation", err)
		}
	})

	t.Run("terminal task is returned", func(t *testing.T) {
		manager := newManager(t, processorFunc(func(
			context.Context,
			*taskmanager.ExecContext,
		) (<-chan protocol.StreamEvent, error) {
			return eventChannel(statusEvent(protocol.TaskStateCompleted, "done")), nil
		}))
		response, err := manager.OnSendMessage(context.Background(), request)
		if err != nil {
			t.Fatalf("OnSendMessage failed: %v", err)
		}
		if task := response.GetTask(); task == nil || task.Status.State != protocol.TaskStateCompleted {
			t.Fatalf("result = %#v, want completed Task", response.Result)
		}
	})
}

func TestOnSendMessage_RejectsUnsupportedRequestFeatures(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		t.Fatal("processor must not be called")
		return nil, nil
	}))

	t.Run("continuation", func(t *testing.T) {
		request := messageParams("continue")
		taskID := "task-1"
		request.Message.TaskID = &taskID
		_, err := manager.OnSendMessage(context.Background(), request)
		if !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
			t.Fatalf("error = %v, want task not found", err)
		}
	})

	t.Run("push config", func(t *testing.T) {
		request := messageParams("push")
		request.Configuration = &protocol.SendMessageConfiguration{
			PushConfig: &protocol.TaskPushNotificationConfig{},
		}
		_, err := manager.OnSendMessage(context.Background(), request)
		if !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
			t.Fatalf("error = %v, want push unsupported", err)
		}
	})
}

func TestOnSendMessage_RejectsSuspendedTask(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(statusEvent(protocol.TaskStateInputRequired, "more input")), nil
	}))
	_, err := manager.OnSendMessage(context.Background(), messageParams("run"))
	if !errors.Is(err, taskmanager.ErrUnsupportedOperationSentinel) {
		t.Fatalf("error = %v, want unsupported operation", err)
	}
}

func TestOnSendMessage_SetupAndContractFailures(t *testing.T) {
	processorErr := errors.New("processor unavailable")
	tests := []struct {
		name      string
		processor taskmanager.MessageProcessor
		want      error
	}{
		{
			name: "processor error",
			processor: processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return nil, processorErr
			}),
			want: processorErr,
		},
		{
			name: "nil channel",
			processor: processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return nil, nil
			}),
			want: taskmanager.ErrInvalidAgentResponseSentinel,
		},
		{
			name: "empty channel",
			processor: processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return eventChannel(), nil
			}),
			want: taskmanager.ErrInvalidAgentResponseSentinel,
		},
		{
			name: "forbidden Task snapshot",
			processor: processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return eventChannel(&protocol.Task{}), nil
			}),
			want: taskmanager.ErrInvalidAgentResponseSentinel,
		},
		{
			name: "artifact without ID",
			processor: processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return eventChannel(&protocol.TaskArtifactUpdateEvent{}), nil
			}),
			want: taskmanager.ErrInvalidAgentResponseSentinel,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newManager(t, test.processor)
			_, err := manager.OnSendMessage(context.Background(), messageParams("hello"))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestOnSendMessage_RequestCancellationCancelsProcessor(t *testing.T) {
	processorStarted := make(chan struct{})
	processorCanceled := make(chan struct{})
	manager := newManager(t, processorFunc(func(
		ctx context.Context,
		_ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		close(processorStarted)
		out := make(chan protocol.StreamEvent)
		go func() {
			<-ctx.Done()
			close(processorCanceled)
			close(out)
		}()
		return out, nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := manager.OnSendMessage(ctx, messageParams("hello"))
		result <- err
	}()
	waitSignal(t, processorStarted, "processor start")
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnSendMessage")
	}
	waitSignal(t, processorCanceled, "processor cancellation")
}

func TestOnSendMessageStream_DirectMessage(t *testing.T) {
	processorCanceled := make(chan struct{})
	producerDone := make(chan struct{})
	manager := newManager(t, processorFunc(func(
		ctx context.Context,
		_ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			defer close(producerDone)
			out <- protocol.NewAgentText("reply")
			<-ctx.Done()
			close(processorCanceled)
			out <- protocol.NewAgentText("discarded")
		}()
		return out, nil
	}))

	stream, err := manager.OnSendMessageStream(context.Background(), messageParams("hello"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	responses := collectStream(t, stream)
	if len(responses) != 1 || messageText(responses[0].GetMessage()) != "reply" {
		t.Fatalf("responses = %+v, want one direct Message", responses)
	}
	waitSignal(t, processorCanceled, "processor cancellation")
	waitSignal(t, producerDone, "producer drain")
}

func TestOnSendMessageStream_RequestLocalTask(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(
			statusEvent(protocol.TaskStateWorking, "working"),
			protocol.NewAgentText("progress"),
			artifactEvent("artifact-1", "result", false),
			statusEvent(protocol.TaskStateCompleted, "done"),
		), nil
	}))

	stream, err := manager.OnSendMessageStream(context.Background(), messageParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	responses := collectStream(t, stream)
	if len(responses) != 5 {
		t.Fatalf("response count = %d, want 5", len(responses))
	}
	initial := responses[0].GetTask()
	working := responses[1].GetStatusUpdate()
	message := responses[2].GetMessage()
	artifact := responses[3].GetArtifactUpdate()
	completed := responses[4].GetStatusUpdate()
	if initial == nil || initial.Status.State != protocol.TaskStateSubmitted {
		t.Fatalf("initial response = %#v, want submitted Task", responses[0].Result)
	}
	if working == nil || working.Status.State != protocol.TaskStateWorking {
		t.Fatalf("working response = %#v", responses[1].Result)
	}
	if message == nil || messageText(message) != "progress" || message.TaskID == nil ||
		*message.TaskID != initial.ID {
		t.Fatalf("message response = %#v", responses[2].Result)
	}
	if artifact == nil || artifact.Artifact.ArtifactID != "artifact-1" {
		t.Fatalf("artifact response = %#v", responses[3].Result)
	}
	if completed == nil || completed.Status.State != protocol.TaskStateCompleted || !completed.Final {
		t.Fatalf("completed response = %#v", responses[4].Result)
	}
	for i := 1; i < len(responses); i++ {
		var taskID, contextID string
		if event := responses[i].GetStatusUpdate(); event != nil {
			taskID, contextID = event.TaskID, event.ContextID
		} else if event := responses[i].GetArtifactUpdate(); event != nil {
			taskID, contextID = event.TaskID, event.ContextID
		} else if event := responses[i].GetMessage(); event != nil {
			taskID = *event.TaskID
			contextID = *event.ContextID
		}
		if taskID != initial.ID || contextID != initial.ContextID {
			t.Errorf("response[%d] IDs = %q/%q, want %q/%q",
				i, taskID, contextID, initial.ID, initial.ContextID)
		}
	}
}

func TestOnSendMessageStream_NonTerminalCloseEmitsFailure(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(statusEvent(protocol.TaskStateWorking, "working")), nil
	}))

	stream, err := manager.OnSendMessageStream(context.Background(), messageParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	responses := collectStream(t, stream)
	if len(responses) != 3 {
		t.Fatalf("response count = %d, want 3", len(responses))
	}
	failed := responses[2].GetStatusUpdate()
	if failed == nil || failed.Status.State != protocol.TaskStateFailed || !failed.Final {
		t.Fatalf("final response = %#v, want failed status", responses[2].Result)
	}
}

func TestOnSendMessageStream_LaterViolationEmitsFailure(t *testing.T) {
	foreignTaskID := "foreign-task"
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(
			statusEvent(protocol.TaskStateWorking, "working"),
			&protocol.Message{
				TaskID: &foreignTaskID,
				Parts:  []*protocol.Part{protocol.NewTextPart("invalid")},
			},
		), nil
	}))

	stream, err := manager.OnSendMessageStream(context.Background(), messageParams("run"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	responses := collectStream(t, stream)
	if len(responses) != 3 {
		t.Fatalf("response count = %d, want 3", len(responses))
	}
	failed := responses[2].GetStatusUpdate()
	if failed == nil || failed.Status.State != protocol.TaskStateFailed {
		t.Fatalf("final response = %#v, want failed status", responses[2].Result)
	}
}

func TestOnSendMessageStream_FirstEventFailureIsReturned(t *testing.T) {
	tests := []struct {
		name  string
		event protocol.StreamEvent
		want  error
	}{
		{name: "Task snapshot", event: &protocol.Task{}, want: taskmanager.ErrInvalidAgentResponseSentinel},
		{name: "nil Message", event: (*protocol.Message)(nil), want: taskmanager.ErrInvalidAgentResponseSentinel},
		{name: "suspended Task", event: statusEvent(protocol.TaskStateAuthRequired, "auth"), want: taskmanager.ErrUnsupportedOperationSentinel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newManager(t, processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return eventChannel(test.event), nil
			}))
			_, err := manager.OnSendMessageStream(context.Background(), messageParams("run"))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestTaskAndPushMethods(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(protocol.NewAgentText("unused")), nil
	}))
	ctx := context.Background()

	if _, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: "task-1"}); !errors.Is(
		err, taskmanager.ErrTaskNotFoundSentinel,
	) {
		t.Errorf("OnGetTask error = %v", err)
	}
	if _, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{ID: "task-1"}); !errors.Is(
		err, taskmanager.ErrTaskNotFoundSentinel,
	) {
		t.Errorf("OnCancelTask error = %v", err)
	}
	list, err := manager.OnListTasks(ctx, protocol.ListTasksParams{})
	if err != nil || list == nil || list.Tasks == nil || len(list.Tasks) != 0 ||
		list.PageSize != taskmanager.ListTasksDefaultPageSize || list.TotalSize != 0 || list.NextPageToken != "" {
		t.Errorf("OnListTasks = %+v, %v", list, err)
	}
	for _, params := range []protocol.ListTasksParams{
		{PageSize: intPtr(0)},
		{PageSize: intPtr(taskmanager.ListTasksMaxPageSize + 1)},
		{HistoryLength: intPtr(-1)},
	} {
		if _, err := manager.OnListTasks(ctx, params); !errors.Is(err, taskmanager.ErrInvalidParamsSentinel) {
			t.Errorf("OnListTasks(%+v) error = %v, want invalid params", params, err)
		}
	}
	if _, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{ID: "task-1"}); !errors.Is(
		err, taskmanager.ErrTaskNotFoundSentinel,
	) {
		t.Errorf("OnResubscribe error = %v", err)
	}

	if _, err := manager.OnPushNotificationSet(
		ctx, protocol.TaskPushNotificationConfig{},
	); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Errorf("OnPushNotificationSet error = %v", err)
	}
	if _, err := manager.OnPushNotificationGet(
		ctx, protocol.GetTaskPushNotificationConfigParams{},
	); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Errorf("OnPushNotificationGet error = %v", err)
	}
	if _, err := manager.OnPushNotificationList(
		ctx, protocol.ListTaskPushNotificationConfigsParams{},
	); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Errorf("OnPushNotificationList error = %v", err)
	}
	if err := manager.OnPushNotificationDelete(
		ctx, protocol.DeleteTaskPushNotificationConfigParams{},
	); !errors.Is(err, taskmanager.ErrPushNotificationNotSupportedSentinel) {
		t.Errorf("OnPushNotificationDelete error = %v", err)
	}
}
