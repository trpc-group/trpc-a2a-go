// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates the v2 task lifecycle from an A2A client.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func main() {
	agentURL := flag.String("agent", "http://localhost:8080/", "A2A agent URL")
	flag.Parse()

	c, err := client.NewA2AClient(*agentURL, client.WithTimeout(10*time.Second))
	if err != nil {
		log.Fatalf("Create client: %v", err)
	}
	ctx := context.Background()

	fmt.Println("=== blocking message/send ===")
	blocking := sendTask(ctx, c, protocol.SendMessageParams{Message: userMessage("hello from v2")})
	fmt.Printf("task=%s state=%s result=%q\n\n", blocking.ID, blocking.Status.State, firstArtifactText(blocking))

	fmt.Println("=== input-required continuation ===")
	pending := sendTask(ctx, c, protocol.SendMessageParams{Message: userMessage("profile")})
	if pending.Status.State != protocol.TaskStateInputRequired {
		log.Fatalf("Expected INPUT_REQUIRED, got %s", pending.Status.State)
	}
	fmt.Printf("task=%s state=%s question=%q\n", pending.ID, pending.Status.State, statusText(pending))

	followUp := userMessage("Ada")
	followUp.TaskID = &pending.ID
	completed := sendTask(ctx, c, protocol.SendMessageParams{Message: followUp})
	if completed.ID != pending.ID {
		log.Fatalf("Continuation created task %s; expected %s", completed.ID, pending.ID)
	}
	fmt.Printf("same task=%s state=%s result=%q\n\n", completed.ID, completed.Status.State, firstArtifactText(completed))

	fmt.Println("=== tasks/get and tasks/list ===")
	historyLength := 10
	got, err := c.GetTasks(ctx, protocol.TaskQueryParams{ID: completed.ID, HistoryLength: &historyLength})
	if err != nil {
		log.Fatalf("Get task: %v", err)
	}
	fmt.Printf("get: task=%s state=%s history=%d\n", got.ID, got.Status.State, len(got.History))

	includeArtifacts := true
	listed, err := c.ListTasks(ctx, protocol.ListTasksParams{
		ContextID:        completed.ContextID,
		IncludeArtifacts: &includeArtifacts,
	})
	if err != nil {
		log.Fatalf("List tasks: %v", err)
	}
	fmt.Printf("list: context=%s tasks=%d\n\n", completed.ContextID, len(listed.Tasks))

	fmt.Println("=== tasks/cancel ===")
	returnImmediately := true
	running := sendTask(ctx, c, protocol.SendMessageParams{
		Message: userMessage("wait"),
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	fmt.Printf("started: task=%s state=%s\n", running.ID, running.Status.State)

	snapshot, err := c.CancelTasks(ctx, protocol.TaskIDParams{ID: running.ID})
	if err != nil {
		log.Fatalf("Cancel task: %v", err)
	}
	fmt.Printf("cancel response: state=%s\n", snapshot.Status.State)
	canceled := waitForState(ctx, c, running.ID, protocol.TaskStateCanceled)
	fmt.Printf("stored result: task=%s state=%s\n", canceled.ID, canceled.Status.State)
}

func sendTask(ctx context.Context, c *client.A2AClient, params protocol.SendMessageParams) *protocol.Task {
	response, err := c.SendMessage(ctx, params)
	if err != nil {
		log.Fatalf("Send message: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		log.Fatalf("Expected a task response, got %+v", response)
	}
	return task
}

func waitForState(
	ctx context.Context,
	c *client.A2AClient,
	taskID string,
	want protocol.TaskState,
) *protocol.Task {
	deadline := time.Now().Add(2 * time.Second)
	for {
		task, err := c.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})
		if err != nil {
			log.Fatalf("Get task %s: %v", taskID, err)
		}
		if task.Status.State == want {
			return task
		}
		if time.Now().After(deadline) {
			log.Fatalf("Task %s did not reach %s; current state is %s", taskID, want, task.Status.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func userMessage(text string) protocol.Message {
	return protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(text)},
	)
}

func statusText(task *protocol.Task) string {
	if task.Status.Message == nil {
		return ""
	}
	return firstText(task.Status.Message.Parts)
}

func firstArtifactText(task *protocol.Task) string {
	if len(task.Artifacts) == 0 {
		return ""
	}
	return firstText(task.Artifacts[0].Parts)
}

func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}
