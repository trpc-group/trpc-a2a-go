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

func TestOnSendMessage_MessageOnly(t *testing.T) {
	var captured *taskmanager.ExecContext
	manager := newManager(t, processorFunc(func(
		_ context.Context,
		ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		captured = ec
		return eventChannel(
			&protocol.Message{
				Role:  protocol.MessageRoleUser,
				Parts: []*protocol.Part{protocol.NewTextPart("first")},
			},
			&protocol.Message{
				Parts: []*protocol.Part{protocol.NewTextPart("last")},
			},
		), nil
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
	if message == nil {
		t.Fatalf("result = %#v, want Message", response.Result)
	}
	if got := messageText(message); got != "last" {
		t.Fatalf("message text = %q, want last", got)
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
	if captured.TaskID == "" {
		t.Error("ExecContext task ID was not pre-allocated")
	}
	if captured.Task != nil {
		t.Errorf("ExecContext task = %#v, want nil", captured.Task)
	}
	if captured.History != nil {
		t.Errorf("ExecContext history = %#v, want nil", captured.History)
	}
	if captured.Tenant != request.Tenant {
		t.Errorf("ExecContext tenant = %q, want %q", captured.Tenant, request.Tenant)
	}
	if captured.Message.MessageID == "" {
		t.Error("input message ID was not generated")
	}
	if captured.Message.ContextID == nil ||
		*captured.Message.ContextID != captured.ContextID {
		t.Error("input message and ExecContext context IDs differ")
	}
	if *message.ContextID != captured.ContextID {
		t.Errorf("output context ID = %q, want %q", *message.ContextID, captured.ContextID)
	}
	if len(captured.AcceptedOutputModes) != 2 ||
		captured.AcceptedOutputModes[1] != "application/json" {
		t.Errorf("accepted output modes = %#v", captured.AcceptedOutputModes)
	}
}

func TestOnSendMessage_SetupFailures(t *testing.T) {
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

func TestOnSendMessage_RejectsTaskFeatures(t *testing.T) {
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
		if !errors.Is(err, taskmanager.ErrUnsupportedOperationSentinel) {
			t.Fatalf("error = %v, want unsupported operation", err)
		}
	})

	t.Run("return immediately", func(t *testing.T) {
		request := messageParams("background")
		returnImmediately := true
		request.Configuration = &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		}
		_, err := manager.OnSendMessage(context.Background(), request)
		if !errors.Is(err, taskmanager.ErrUnsupportedOperationSentinel) {
			t.Fatalf("error = %v, want unsupported operation", err)
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

func TestOnSendMessage_RejectsNonMessageEventsAndDrains(t *testing.T) {
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
			out <- &protocol.TaskStatusUpdateEvent{
				Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
			}
			<-ctx.Done()
			close(processorCanceled)
			out <- protocol.NewAgentText("must be drained")
		}()
		return out, nil
	}))

	_, err := manager.OnSendMessage(context.Background(), messageParams("hello"))
	if !errors.Is(err, taskmanager.ErrInvalidAgentResponseSentinel) {
		t.Fatalf("error = %v, want invalid agent response", err)
	}
	waitSignal(t, processorCanceled, "processor cancellation")
	waitSignal(t, producerDone, "producer drain")
}

func TestOnSendMessage_RejectsInvalidMessages(t *testing.T) {
	foreignContext := "foreign-context"
	taskID := "task-1"
	tests := []struct {
		name    string
		message *protocol.Message
	}{
		{
			name: "foreign context",
			message: &protocol.Message{
				ContextID: &foreignContext,
				Parts:     []*protocol.Part{protocol.NewTextPart("reply")},
			},
		},
		{
			name: "task-bound message",
			message: &protocol.Message{
				TaskID: &taskID,
				Parts:  []*protocol.Part{protocol.NewTextPart("reply")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newManager(t, processorFunc(func(
				context.Context,
				*taskmanager.ExecContext,
			) (<-chan protocol.StreamEvent, error) {
				return eventChannel(test.message), nil
			}))
			_, err := manager.OnSendMessage(context.Background(), messageParams("hello"))
			if !errors.Is(err, taskmanager.ErrInvalidAgentResponseSentinel) {
				t.Fatalf("error = %v, want invalid agent response", err)
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

func TestOnSendMessageStream_MessageOnly(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(
			&protocol.Message{Parts: []*protocol.Part{protocol.NewTextPart("one")}},
			&protocol.Message{Parts: []*protocol.Part{protocol.NewTextPart("two")}},
		), nil
	}))

	stream, err := manager.OnSendMessageStream(context.Background(), messageParams("hello"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	responses := collectStream(t, stream)
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want 2", len(responses))
	}
	for i, want := range []string{"one", "two"} {
		message := responses[i].GetMessage()
		if got := messageText(message); got != want {
			t.Errorf("response[%d] text = %q, want %q", i, got, want)
		}
		if message == nil || message.Role != protocol.MessageRoleAgent {
			t.Errorf("response[%d] = %#v, want agent Message", i, responses[i].Result)
		}
	}
}

func TestOnSendMessageStream_RequestCancellationCancelsProcessor(t *testing.T) {
	processorCanceled := make(chan struct{})
	manager := newManager(t, processorFunc(func(
		ctx context.Context,
		_ *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent)
		go func() {
			defer close(out)
			out <- protocol.NewAgentText("first")
			<-ctx.Done()
			close(processorCanceled)
		}()
		return out, nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := manager.OnSendMessageStream(ctx, messageParams("hello"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	select {
	case response := <-stream:
		if got := messageText(response.GetMessage()); got != "first" {
			t.Fatalf("first response = %q, want first", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first response")
	}
	cancel()
	if responses := collectStream(t, stream); len(responses) != 0 {
		t.Fatalf("responses after cancellation = %d, want 0", len(responses))
	}
	waitSignal(t, processorCanceled, "processor cancellation")
}

func TestOnSendMessageStream_ContractViolationClosesAndDrains(t *testing.T) {
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
			out <- &protocol.TaskArtifactUpdateEvent{}
			<-ctx.Done()
			close(processorCanceled)
			out <- protocol.NewAgentText("must be drained")
		}()
		return out, nil
	}))

	stream, err := manager.OnSendMessageStream(context.Background(), messageParams("hello"))
	if err != nil {
		t.Fatalf("OnSendMessageStream failed: %v", err)
	}
	if responses := collectStream(t, stream); len(responses) != 0 {
		t.Fatalf("responses = %d, want 0", len(responses))
	}
	waitSignal(t, processorCanceled, "processor cancellation")
	waitSignal(t, producerDone, "producer drain")
}

func TestTaskAndPushMethodsAreUnsupported(t *testing.T) {
	manager := newManager(t, processorFunc(func(
		context.Context,
		*taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		return eventChannel(protocol.NewAgentText("unused")), nil
	}))
	ctx := context.Background()

	if _, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{}); !errors.Is(
		err, taskmanager.ErrUnsupportedOperationSentinel,
	) {
		t.Errorf("OnGetTask error = %v", err)
	}
	if _, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{}); !errors.Is(
		err, taskmanager.ErrUnsupportedOperationSentinel,
	) {
		t.Errorf("OnCancelTask error = %v", err)
	}
	if _, err := manager.OnListTasks(ctx, protocol.ListTasksParams{}); !errors.Is(
		err, taskmanager.ErrUnsupportedOperationSentinel,
	) {
		t.Errorf("OnListTasks error = %v", err)
	}
	if _, err := manager.OnResubscribe(ctx, protocol.TaskIDParams{}); !errors.Is(
		err, taskmanager.ErrUnsupportedOperationSentinel,
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
