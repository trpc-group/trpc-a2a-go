// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent. All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command client calls the Redis coalesced-persistence example server.
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
	serverURL := flag.String("addr", "http://localhost:8081/", "A2A server URL")
	text := flag.String("text", "generate a large streamed artifact", "request text")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	flag.Parse()

	a2aClient, err := client.NewA2AClient(*serverURL, client.WithTimeout(*timeout))
	if err != nil {
		log.Fatalf("create A2A client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	events, err := a2aClient.StreamMessage(ctx, protocol.SendMessageParams{
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart(*text)},
		),
	})
	if err != nil {
		log.Fatalf("start message/stream: %v", err)
	}

	var taskID string
	var artifactEvents int
	var receivedBytes int
	for event := range events {
		switch {
		case event.GetTask() != nil:
			taskID = event.GetTask().ID
			fmt.Printf("task snapshot: id=%s state=%s\n", taskID, event.GetTask().Status.State)
		case event.GetArtifactUpdate() != nil:
			artifactEvents++
			receivedBytes += textBytes(event.GetArtifactUpdate().Artifact.Parts)
			if artifactEvents == 1 || artifactEvents%16 == 0 {
				fmt.Printf("artifact events: %d, received: %d bytes\n", artifactEvents, receivedBytes)
			}
		case event.GetStatusUpdate() != nil:
			fmt.Printf("status: %s\n", event.GetStatusUpdate().Status.State)
		case event.GetMessage() != nil:
			fmt.Printf("message: %s\n", firstText(event.GetMessage().Parts))
		}
	}
	if err := ctx.Err(); err != nil {
		log.Fatalf("stream ended with context error: %v", err)
	}
	if taskID == "" {
		log.Fatal("stream ended without a Task snapshot")
	}

	finalTask, err := a2aClient.GetTasks(context.Background(), protocol.TaskQueryParams{ID: taskID})
	if err != nil {
		log.Fatalf("tasks/get: %v", err)
	}
	fmt.Printf(
		"done: task=%s state=%s artifact-events=%d received=%d bytes stored-parts=%d\n",
		finalTask.ID,
		finalTask.Status.State,
		artifactEvents,
		receivedBytes,
		artifactPartCount(finalTask),
	)
}

func textBytes(parts []*protocol.Part) int {
	total := 0
	for _, part := range parts {
		total += len(part.TextContent())
	}
	return total
}

func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

func artifactPartCount(task *protocol.Task) int {
	if task == nil || len(task.Artifacts) == 0 {
		return 0
	}
	return len(task.Artifacts[0].Parts)
}
