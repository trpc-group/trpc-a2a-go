// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the client side of the v2 streaming example.
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func main() {
	agentURL := flag.String("url", "http://localhost:8089/", "agent JSON-RPC URL")
	text := flag.String("text", "Streaming makes incremental results visible to clients.", "text to stream")
	cancelAfter := flag.Int("cancel-after", 0, "call CancelTask after this many chunks (0 completes normally)")
	flag.Parse()

	if *cancelAfter < 0 {
		log.Fatalf("-cancel-after must be zero or greater")
	}

	c, err := client.NewA2AClient(*agentURL)
	if err != nil {
		log.Fatalf("Create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start work without holding the unary request open. The first task event
	// gives this response the task ID needed by SubscribeToTask.
	returnImmediately := true
	response, err := c.SendMessage(ctx, protocol.SendMessageParams{
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart(*text)},
		),
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	if err != nil {
		log.Fatalf("SendMessage: %v", err)
	}
	task := response.GetTask()
	if task == nil {
		log.Fatalf("SendMessage returned no task: %+v", response)
	}
	fmt.Printf("SendMessage returned immediately: task=%s state=%s\n", task.ID, task.Status.State)

	// ResubscribeTask is the Go API name; on the v2 wire it invokes the
	// SubscribeToTask method. Its first frame is the current task snapshot.
	events, err := c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		log.Fatalf("SubscribeToTask: %v", err)
	}

	var (
		artifactID      string
		assembled       strings.Builder
		chunks          int
		cancelRequested bool
	)
	for event := range events {
		switch {
		case event.GetTask() != nil:
			snapshot := event.GetTask()
			fmt.Printf("SubscribeToTask snapshot: state=%s artifacts=%d\n",
				snapshot.Status.State, len(snapshot.Artifacts))

		case event.GetArtifactUpdate() != nil:
			update := event.GetArtifactUpdate()
			if artifactID == "" {
				artifactID = update.Artifact.ArtifactID
			} else if update.Artifact.ArtifactID != artifactID {
				log.Fatalf("artifact ID changed: %s -> %s", artifactID, update.Artifact.ArtifactID)
			}

			chunks++
			appendChunk := update.Append != nil && *update.Append
			if chunks == 1 && appendChunk {
				log.Fatalf("first artifact chunk unexpectedly has append=true")
			}
			if chunks > 1 && !appendChunk {
				log.Fatalf("artifact chunk %d must have append=true", chunks)
			}

			chunk := firstText(update.Artifact.Parts)
			assembled.WriteString(chunk)
			lastChunk := update.LastChunk != nil && *update.LastChunk
			fmt.Printf("artifact chunk %d: id=%s append=%t lastChunk=%t text=%q\n",
				chunks, artifactID, appendChunk, lastChunk, chunk)

			if *cancelAfter > 0 && chunks >= *cancelAfter && !cancelRequested {
				canceled, err := c.CancelTasks(ctx, protocol.TaskIDParams{ID: task.ID})
				if err != nil {
					log.Fatalf("CancelTask: %v", err)
				}
				cancelRequested = true
				fmt.Printf("CancelTask requested: response state=%s\n", canceled.Status.State)
			}

		case event.GetStatusUpdate() != nil:
			status := event.GetStatusUpdate().Status
			fmt.Printf("status: %s\n", status.State)
		}
	}

	final, err := c.GetTasks(ctx, protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		log.Fatalf("GetTask: %v", err)
	}
	fmt.Printf("final task: state=%s chunks=%d assembled=%q\n",
		final.Status.State, chunks, assembled.String())
}

func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}
