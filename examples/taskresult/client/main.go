// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates collecting a task's result after a non-blocking send.
//
// It sends a message with returnImmediately=true, which returns the task id
// right away while the agent keeps working, and then fetches the result two
// ways:
//   - tasks/get (GetTasks): poll the task until it reaches a terminal state;
//   - tasks/resubscribe (ResubscribeTask): stream the remaining events until the
//     round ends.
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
	host := flag.String("host", "localhost:8082", "server address")
	flag.Parse()

	a2aClient, err := client.NewA2AClient(
		fmt.Sprintf("http://%s/", *host),
		client.WithTimeout(60*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	demoSendThenPoll(a2aClient)
	demoSendThenSubscribe(a2aClient)
}

// demoSendThenPoll sends a message without waiting, then polls tasks/get until
// the task finishes and prints the result.
func demoSendThenPoll(c *client.A2AClient) {
	fmt.Println("=== send (returnImmediately) then tasks/get poll ===")
	ctx := context.Background()

	task := sendReturnImmediately(ctx, c, "hello from the polling client")
	fmt.Printf("sent; task id=%s state=%s\n", task.ID, task.Status.State)

	// Poll until the task reaches a terminal state.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := c.GetTasks(ctx, protocol.TaskQueryParams{ID: task.ID})
		if err != nil {
			log.Fatalf("GetTasks failed: %v", err)
		}
		fmt.Printf("  poll: state=%s\n", got.Status.State)
		if isTerminal(got.Status.State) {
			printResult(got)
			break
		}
		if time.Now().After(deadline) {
			log.Fatalf("timed out waiting for task %s to finish", task.ID)
		}
		time.Sleep(400 * time.Millisecond)
	}
	fmt.Println()
}

// demoSendThenSubscribe sends a message without waiting, then resubscribes to
// stream the remaining events until the round ends.
func demoSendThenSubscribe(c *client.A2AClient) {
	fmt.Println("=== send (returnImmediately) then tasks/resubscribe ===")
	ctx := context.Background()

	task := sendReturnImmediately(ctx, c, "hello from the subscribing client")
	fmt.Printf("sent; task id=%s state=%s\n", task.ID, task.Status.State)

	events, err := c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		log.Fatalf("ResubscribeTask failed: %v", err)
	}

	for event := range events {
		switch {
		case event.GetTask() != nil:
			fmt.Printf("  snapshot: state=%s\n", event.GetTask().Status.State)
		case event.GetStatusUpdate() != nil:
			su := event.GetStatusUpdate()
			fmt.Printf("  status: %s%s\n", su.Status.State, statusText(su))
		case event.GetArtifactUpdate() != nil:
			fmt.Printf("  artifact: %s\n", firstText(event.GetArtifactUpdate().Artifact.Parts))
		case event.GetMessage() != nil:
			fmt.Printf("  message: %s\n", firstText(event.GetMessage().Parts))
		}
	}
	fmt.Println("stream closed (round ended)")
	fmt.Println()
}

// sendReturnImmediately sends text with returnImmediately=true and returns the
// immediate task snapshot.
func sendReturnImmediately(ctx context.Context, c *client.A2AClient, text string) *protocol.Task {
	returnImmediately := true
	resp, err := c.SendMessage(ctx, protocol.SendMessageParams{
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart(text)},
		),
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		log.Fatalf("expected an immediate task snapshot, got a message: %s",
			firstText(resp.GetMessage().Parts))
	}
	return task
}

// printResult prints a terminal task's status message and artifacts.
func printResult(task *protocol.Task) {
	fmt.Printf("  final state=%s\n", task.Status.State)
	if task.Status.Message != nil {
		fmt.Printf("  result message: %s\n", firstText(task.Status.Message.Parts))
	}
	for _, artifact := range task.Artifacts {
		fmt.Printf("  artifact: %s\n", firstText(artifact.Parts))
	}
}

// isTerminal reports whether the state is a terminal task state.
func isTerminal(state protocol.TaskState) bool {
	switch state {
	case protocol.TaskStateCompleted,
		protocol.TaskStateFailed,
		protocol.TaskStateCanceled,
		protocol.TaskStateRejected:
		return true
	default:
		return false
	}
}

// statusText renders the status message text with a leading space, if any.
func statusText(su *protocol.TaskStatusUpdateEvent) string {
	if su.Status.Message == nil {
		return ""
	}
	if t := firstText(su.Status.Message.Parts); t != "" {
		return " - " + t
	}
	return ""
}

// firstText extracts the first non-empty text content from parts.
func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}
