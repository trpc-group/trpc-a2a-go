// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main is a legacy-wire A2A client: it talks the v0.2.x JSON-RPC
// protocol (message/send, tasks/get, message/stream) through the compat/v0
// client, against a server that mounts the compat handler.
//
// It demonstrates the legacy defaults the compat layer preserves — most
// importantly that a configuration-less message/send answers immediately
// with the working task snapshot (the v0.2.x non-blocking default), where
// the v1.0 wire would block until the round ends.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func main() {
	var agentURL string
	var message string
	flag.StringVar(&agentURL, "agent", "http://localhost:8080/", "Agent URL")
	flag.StringVar(&message, "message", "hello legacy world", "Text to send")
	flag.Parse()

	client, err := v0.NewClient(agentURL)
	if err != nil {
		log.Fatalf("Failed to create legacy client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The compat client takes v1 types and converts to the legacy wire
	// underneath, so the same params shape works for every demo below.
	params := protocol.SendMessageParams{
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []*protocol.Part{protocol.NewTextPart(message)},
		},
	}

	// --- 1. Configuration-less legacy send: the v0.2.x non-blocking default.
	// The compat layer maps the absent configuration to returnImmediately, so
	// this returns the working snapshot right away while the round runs on.
	fmt.Println("=== legacy message/send (no configuration: v0 non-blocking default) ===")
	resp, err := client.SendMessage(ctx, params)
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}
	task := resp.GetTask()
	if task == nil {
		log.Fatalf("expected a task result, got %+v", resp)
	}
	fmt.Printf("immediate answer: task %s state=%s\n", task.ID, task.Status.State)

	// Poll tasks/get until the round completes — the v0 wait-by-polling style.
	for !task.Status.State.Terminal() {
		time.Sleep(500 * time.Millisecond)
		task, err = client.GetTasks(ctx, protocol.TaskQueryParams{ID: task.ID})
		if err != nil {
			log.Fatalf("GetTasks failed: %v", err)
		}
		fmt.Printf("poll: state=%s\n", task.Status.State)
	}
	printArtifacts(task)

	// --- 2. Explicit blocking send: legacy blocking=true maps to v1
	// returnImmediately=false — one call, final task in the response.
	fmt.Println("=== legacy message/send (blocking=true) ===")
	returnImmediately := false
	params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &returnImmediately}
	resp, err = client.SendMessage(ctx, params)
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}
	if task := resp.GetTask(); task != nil {
		fmt.Printf("final answer in one call: state=%s\n", task.Status.State)
		printArtifacts(task)
	}

	// --- 3. Legacy streaming: message/stream over SSE, converted frame kinds.
	fmt.Println("=== legacy message/stream ===")
	params.Configuration = nil
	events, err := client.StreamMessage(ctx, params)
	if err != nil {
		log.Fatalf("StreamMessage failed: %v", err)
	}
	for event := range events {
		switch {
		case event.GetStatusUpdate() != nil:
			fmt.Printf("stream: status=%s final=%t\n",
				event.GetStatusUpdate().Status.State, event.GetStatusUpdate().Final)
		case event.GetArtifactUpdate() != nil:
			fmt.Printf("stream: artifact %s\n", event.GetArtifactUpdate().Artifact.ArtifactID)
		case event.GetMessage() != nil:
			fmt.Printf("stream: message\n")
		}
	}
	fmt.Println("stream closed")
}

// printArtifacts prints the artifacts of a completed task.
func printArtifacts(task *protocol.Task) {
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			if text := part.TextContent(); text != "" {
				fmt.Printf("artifact: %s\n", text)
			}
		}
	}
}
