// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates how message/send (non-streaming) and
// message/stream (streaming) consume the SAME server-side MessageProcessor:
// the caller picks the endpoint; the processor code does not change.
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
	host := flag.String("host", "localhost:8080", "server address")
	flag.Parse()

	a2aClient, err := client.NewA2AClient(
		fmt.Sprintf("http://%s/", *host),
		// NOTE: http.Client.Timeout caps the WHOLE response body read, which
		// for message/stream is the entire SSE lifetime. A real long-running
		// streaming agent needs a longer timeout (or none); 30s only suits this
		// toy example where every round finishes in milliseconds.
		client.WithTimeout(30*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	demoNonStreaming(a2aClient)
	demoReturnImmediately(a2aClient)
	demoStreaming(a2aClient)
	demoPureMessage(a2aClient)
}

// demoPureMessage: a message with no text part drives the server's pure-Message
// path — it replies with a validation Message and NO task is created (lazy
// creation). The union carries a Message, not a Task.
func demoPureMessage(c *client.A2AClient) {
	fmt.Println("=== message/send (pure-message path: no task created) ===")
	resp, err := c.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewDataPart(map[string]any{"unsupported": true})},
		),
	})
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}
	switch {
	case resp.GetMessage() != nil:
		fmt.Printf("message reply (no task): %s\n", firstText(resp.GetMessage().Parts))
	case resp.GetTask() != nil:
		fmt.Printf("unexpected task: %s\n", resp.GetTask().Status.State)
	}
	fmt.Println()
}

// demoNonStreaming: one request, one FINAL answer. The intermediate working
// events are consumed by the framework (persisted, fanned out to any
// subscribers) — this caller sees only the derived result: the terminal task
// snapshot with its artifacts, or the reply message for pure-message rounds.
func demoNonStreaming(c *client.A2AClient) {
	fmt.Println("=== message/send (non-streaming, blocking) ===")
	resp, err := c.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: newUserMessage("Hello world!"),
	})
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}

	switch {
	case resp.GetTask() != nil:
		task := resp.GetTask()
		fmt.Printf("final task: state=%s\n", task.Status.State)
		if task.Status.Message != nil {
			fmt.Printf("  status message: %s\n", firstText(task.Status.Message.Parts))
		}
		for _, artifact := range task.Artifacts {
			fmt.Printf("  artifact: %s\n", firstText(artifact.Parts))
		}
	case resp.GetMessage() != nil:
		// The pure-message path (e.g. the server's input-validation reply).
		fmt.Printf("message reply: %s\n", firstText(resp.GetMessage().Parts))
	}
	fmt.Println()
}

// demoReturnImmediately: the non-blocking variant of message/send — returns
// the first persisted task snapshot right away; the result is collected later
// via GetTasks (or SubscribeToTask).
func demoReturnImmediately(c *client.A2AClient) {
	fmt.Println("=== message/send (returnImmediately + poll) ===")
	returnImmediately := true
	resp, err := c.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: newUserMessage("Hello world!"),
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		log.Fatalf("Expected an immediate task snapshot, got %+v", resp)
	}
	fmt.Printf("immediate snapshot: state=%s\n", task.Status.State)

	// Poll for the final state (a real client might resubscribe instead).
	time.Sleep(100 * time.Millisecond)
	final, err := c.GetTasks(context.Background(), protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		log.Fatalf("GetTasks failed: %v", err)
	}
	fmt.Printf("after poll: state=%s artifacts=%d\n\n", final.Status.State, len(final.Artifacts))
}

// demoStreaming: the same request via message/stream — every event as it
// happens; the channel closes when the round ends.
func demoStreaming(c *client.A2AClient) {
	fmt.Println("=== message/stream (streaming) ===")
	events, err := c.StreamMessage(context.Background(), protocol.SendMessageParams{
		Message: newUserMessage("Hello world!"),
	})
	if err != nil {
		log.Fatalf("StreamMessage failed: %v", err)
	}

	for event := range events {
		switch {
		case event.GetStatusUpdate() != nil:
			fmt.Printf("status: %s\n", event.GetStatusUpdate().Status.State)
		case event.GetArtifactUpdate() != nil:
			fmt.Printf("artifact: %s\n", firstText(event.GetArtifactUpdate().Artifact.Parts))
		case event.GetMessage() != nil:
			fmt.Printf("message: %s\n", firstText(event.GetMessage().Parts))
		case event.GetTask() != nil:
			// Task snapshot frames (e.g. the first frame of a resubscribe).
			fmt.Printf("task snapshot: %s\n", event.GetTask().Status.State)
		}
	}
	fmt.Println("stream closed (round ended)")
}

// newUserMessage builds a user text message.
func newUserMessage(text string) protocol.Message {
	return protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(text)},
	)
}

// firstText extracts the first text content from parts.
func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}
