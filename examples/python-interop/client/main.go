// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent. All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the Go client for the Python SDK interoperability example.
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

const (
	tenantA = "tenant-a"
	tenantB = "tenant-b"
)

var agentURL = flag.String("agent", "http://localhost:8080/", "Go or Python A2A server URL")

func main() {
	flag.Parse()

	c, err := client.NewA2AClient(*agentURL, client.WithTimeout(10*time.Second))
	if err != nil {
		panic(fmt.Errorf("create client: %w", err))
	}
	ctx := context.Background()
	sharedContextID := protocol.GenerateContextID()
	sharedMessageID := protocol.GenerateMessageID()
	tasks := make(map[string]*protocol.Task, 2)

	fmt.Println("=== Go client -> A2A server ===")
	fmt.Printf("  server          : %s\n", *agentURL)
	fmt.Printf("  shared contextId: %s\n", sharedContextID)
	fmt.Printf("  shared messageId: %s\n\n", sharedMessageID)

	for _, tenant := range []string{tenantA, tenantB} {
		card, err := c.GetTenantAgentCard(ctx, tenant)
		if err != nil {
			panic(fmt.Errorf("get %s agent card: %w", tenant, err))
		}
		fmt.Printf("--- %s ---\n", tenant)
		fmt.Printf("  name       : %s\n", card.Name)
		fmt.Printf("  description: %s\n", card.Description)
		for _, iface := range card.SupportedInterfaces {
			fmt.Printf("  interface  : binding=%s url=%s tenant=%s\n",
				iface.ProtocolBinding, iface.URL, iface.Tenant)
		}

		fmt.Println("\n  [send]")
		sendText := fmt.Sprintf("hello from Go for %s", tenant)
		fmt.Printf("  input     : %q\n", sendText)
		task, err := streamTask(ctx, c, protocol.SendMessageParams{
			Tenant: tenant,
			Message: userMessage(
				sendText,
				sharedMessageID,
				sharedContextID,
			),
		})
		if err != nil {
			panic(fmt.Errorf("send to %s: %w", tenant, err))
		}
		if task.Status.State != protocol.TaskStateCompleted {
			panic(fmt.Errorf("%s task state is %s", tenant, task.Status.State))
		}
		tasks[tenant] = task
		fmt.Printf("  completed : task=%s result=%q\n", task.ID, firstArtifactText(task))

		fmt.Println("\n  [input-required]")
		fmt.Printf("  input     : %q\n", "profile")
		pending, err := streamTask(ctx, c, protocol.SendMessageParams{
			Tenant:  tenant,
			Message: userMessage("profile", protocol.GenerateMessageID(), protocol.GenerateContextID()),
		})
		if err != nil {
			panic(fmt.Errorf("start %s continuation: %w", tenant, err))
		}
		if pending.Status.State != protocol.TaskStateInputRequired {
			panic(fmt.Errorf("%s profile task state is %s", tenant, pending.Status.State))
		}

		fmt.Printf("  input     : %q (continue task=%s)\n", "Ada", pending.ID)
		followUp := userMessage("Ada", protocol.GenerateMessageID(), pending.ContextID)
		followUp.TaskID = &pending.ID
		completed, err := streamTask(ctx, c, protocol.SendMessageParams{Tenant: tenant, Message: followUp})
		if err != nil {
			panic(fmt.Errorf("continue %s task: %w", tenant, err))
		}
		if completed.ID != pending.ID || completed.Status.State != protocol.TaskStateCompleted {
			panic(fmt.Errorf("%s continuation did not complete task %s", tenant, pending.ID))
		}
		fmt.Printf("  continued : task=%s result=%q\n", completed.ID, firstArtifactText(completed))

		fmt.Println("\n  [get/list]")
		historyLength := 10
		stored, err := c.GetTasks(ctx, protocol.TaskQueryParams{
			Tenant:        tenant,
			ID:            task.ID,
			HistoryLength: &historyLength,
		})
		if err != nil {
			panic(fmt.Errorf("get %s task: %w", tenant, err))
		}
		includeArtifacts := true
		listed, err := c.ListTasks(ctx, protocol.ListTasksParams{
			Tenant:           tenant,
			ContextID:        sharedContextID,
			IncludeArtifacts: &includeArtifacts,
		})
		if err != nil {
			panic(fmt.Errorf("list %s tasks: %w", tenant, err))
		}
		if len(listed.Tasks) != 1 || listed.Tasks[0].ID != task.ID {
			panic(fmt.Errorf("%s list leaked tenant data: %+v", tenant, listed.Tasks))
		}
		fmt.Printf("  get/list  : task=%s history=%d scopedTasks=%d\n\n",
			stored.ID, len(stored.History), len(listed.Tasks))
	}

	fmt.Println("--- tenant isolation ---")
	if err := expectTaskNotFound(ctx, c, tenantB, tasks[tenantA].ID); err != nil {
		panic(err)
	}
	fmt.Printf("  %s cannot read %s task %s\n", tenantB, tenantA, tasks[tenantA].ID)
	if err := expectTaskNotFound(ctx, c, tenantA, tasks[tenantB].ID); err != nil {
		panic(err)
	}
	fmt.Printf("  %s cannot read %s task %s\n\n", tenantA, tenantB, tasks[tenantB].ID)

	fmt.Println("--- cancellation ---")
	returnImmediately := true
	running, err := sendAsyncTask(ctx, c, protocol.SendMessageParams{
		Tenant:  tenantA,
		Message: userMessage("wait", protocol.GenerateMessageID(), protocol.GenerateContextID()),
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	if err != nil {
		panic(fmt.Errorf("start cancellable task: %w", err))
	}
	subscription, err := c.ResubscribeTask(ctx, protocol.TaskIDParams{Tenant: tenantA, ID: running.ID})
	if err != nil {
		panic(fmt.Errorf("subscribe cancellable task: %w", err))
	}
	first, ok := <-subscription
	if !ok || first.GetTask() == nil {
		panic(fmt.Errorf("subscription must start with Task, got %+v", first))
	}
	subscriptionPath := []string{describeStreamEvent(first)}
	if _, err := c.CancelTasks(ctx, protocol.TaskIDParams{Tenant: tenantB, ID: running.ID}); !isTaskNotFound(err) {
		panic(fmt.Errorf("cross-tenant cancel returned %v, want task-not-found", err))
	}
	if _, err := c.CancelTasks(ctx, protocol.TaskIDParams{Tenant: tenantA, ID: running.ID}); err != nil {
		panic(fmt.Errorf("cancel own task: %w", err))
	}
	sawCanceled := false
	for event := range subscription {
		subscriptionPath = append(subscriptionPath, describeStreamEvent(event))
		if update := event.GetStatusUpdate(); update != nil && update.Status.State == protocol.TaskStateCanceled {
			sawCanceled = true
		}
	}
	if !sawCanceled {
		panic(fmt.Errorf("subscription for task %s closed without CANCELED", running.ID))
	}
	canceled, err := c.GetTasks(ctx, protocol.TaskQueryParams{Tenant: tenantA, ID: running.ID})
	if err != nil {
		panic(fmt.Errorf("get canceled task: %w", err))
	}
	fmt.Printf("  subscription: %s\n", strings.Join(subscriptionPath, " -> "))
	fmt.Printf("  %s canceled own task=%s state=%s; %s could not cancel it\n",
		tenantA, canceled.ID, shortState(canceled.Status.State), tenantB)

	fmt.Println("\n=== interoperability and tenant isolation verified ===")
}

func sendAsyncTask(
	ctx context.Context,
	c *client.A2AClient,
	params protocol.SendMessageParams,
) (*protocol.Task, error) {
	response, err := c.SendMessage(ctx, params)
	if err != nil {
		return nil, err
	}
	task := response.GetTask()
	if task == nil {
		return nil, fmt.Errorf("expected task response, got %+v", response)
	}
	return task, nil
}

func streamTask(
	ctx context.Context,
	c *client.A2AClient,
	params protocol.SendMessageParams,
) (*protocol.Task, error) {
	events, err := c.StreamMessage(ctx, params)
	if err != nil {
		return nil, err
	}

	var taskID string
	var path []string
	for event := range events {
		switch {
		case event.GetTask() != nil:
			taskID = event.GetTask().ID
		case event.GetStatusUpdate() != nil:
			taskID = event.GetStatusUpdate().TaskID
		case event.GetArtifactUpdate() != nil:
			taskID = event.GetArtifactUpdate().TaskID
		}
		path = append(path, describeStreamEvent(event))
	}
	if taskID == "" {
		return nil, fmt.Errorf("stream closed without a task ID")
	}
	fmt.Printf("  stream    : %s\n", strings.Join(path, " -> "))

	historyLength := 10
	return c.GetTasks(ctx, protocol.TaskQueryParams{
		Tenant:        params.Tenant,
		ID:            taskID,
		HistoryLength: &historyLength,
	})
}

func describeStreamEvent(event protocol.StreamResponse) string {
	switch {
	case event.GetTask() != nil:
		return fmt.Sprintf("Task(%s)", shortState(event.GetTask().Status.State))
	case event.GetStatusUpdate() != nil:
		return fmt.Sprintf("Status(%s)", shortState(event.GetStatusUpdate().Status.State))
	case event.GetArtifactUpdate() != nil:
		return fmt.Sprintf("Artifact(%s)", event.GetArtifactUpdate().Artifact.ArtifactID)
	case event.GetMessage() != nil:
		return "Message"
	default:
		return "Unknown"
	}
}

func shortState(state protocol.TaskState) string {
	return strings.TrimPrefix(string(state), "TASK_STATE_")
}

func userMessage(text, messageID, contextID string) protocol.Message {
	message := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(text)},
	)
	message.MessageID = messageID
	message.ContextID = &contextID
	return message
}

func expectTaskNotFound(ctx context.Context, c *client.A2AClient, tenant, taskID string) error {
	_, err := c.GetTasks(ctx, protocol.TaskQueryParams{Tenant: tenant, ID: taskID})
	if !isTaskNotFound(err) {
		return fmt.Errorf("%s read task %s or received an unexpected error: %v", tenant, taskID, err)
	}
	return nil
}

func isTaskNotFound(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "-32001") || strings.Contains(text, "task not found") || strings.Contains(text, "http status 404")
}

func firstArtifactText(task *protocol.Task) string {
	if len(task.Artifacts) == 0 {
		return ""
	}
	for _, part := range task.Artifacts[0].Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}
