// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a simple A2A client example.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func main() {
	// Parse command-line flags.
	agentURL := flag.String("agent", "http://localhost:8080/", "Target A2A agent URL")
	timeout := flag.Duration("timeout", 30*time.Second, "Request timeout (e.g., 30s, 1m)")
	message := flag.String("message", "Hello, world!", "Message to send to the agent")
	streaming := flag.Bool("streaming", false, "Use streaming mode (message/stream)")
	flag.Parse()

	// Create A2A client.
	a2aClient, err := client.NewA2AClient(*agentURL, client.WithTimeout(*timeout))
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	// Display connection information.
	log.Infof("Connecting to agent: %s (Timeout: %v)", *agentURL, *timeout)
	log.Infof("Mode: %s", map[bool]string{true: "Streaming", false: "Standard"}[*streaming])

	// Create the message to send using the new constructor.
	userMessage := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(*message)},
	)

	// Create message parameters using the new SendMessageParams structure.
	params := protocol.SendMessageParams{
		Message: userMessage,
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately:   boolPtr(true), // v1.0: replaces Blocking with inverted semantics (true = don't wait)
			AcceptedOutputModes: []string{"text"},
		},
	}

	log.Infof("Sending message with content: %s", *message)

	// Create context for the request.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *streaming {
		// Use streaming mode
		handleStreamingMode(ctx, a2aClient, params)
	} else {
		// Use standard mode
		handleStandardMode(ctx, a2aClient, params)
	}
}

// handleStreamingMode handles streaming message sending and event processing
func handleStreamingMode(ctx context.Context, a2aClient *client.A2AClient, params protocol.SendMessageParams) {
	log.Infof("Starting streaming request...")

	// Send streaming message request
	eventChan, err := a2aClient.StreamMessage(ctx, params)
	if err != nil {
		log.Fatalf("Failed to start streaming: %v", err)
	}

	log.Infof("Processing streaming events...")

	eventCount := 0
	var finalResult string

	// Process streaming events
	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				log.Infof("Stream completed. Total events received: %d", eventCount)
				if finalResult != "" {
					log.Infof("Final result: %s", finalResult)
				}
				return
			}

			eventCount++
			log.Infof("Event %d received: %s", eventCount, getEventDescription(event))

			// Extract final result from completed events
			if result := extractFinalResult(event); result != "" {
				finalResult = result
				log.Infof("Received msg: [Text: %s]", result)
			}

		case <-ctx.Done():
			log.Infof("Request timed out after receiving %d events", eventCount)
			return
		}
	}
}

// handleStandardMode handles standard (non-streaming) message sending
func handleStandardMode(ctx context.Context, a2aClient *client.A2AClient, params protocol.SendMessageParams) {
	messageResult, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		log.Fatalf("Failed to send message: %v", err)
	}

	// Display the result.
	log.Infof("Message sent successfully")

	if msg := messageResult.Message; msg != nil {
		log.Infof("Received message response:")
		printMessage(*msg)
	} else if task := messageResult.Task; task != nil {
		log.Infof("Received task response - ID: %s, State: %s", task.ID, task.Status.State)

		if task.Status.State != protocol.TaskStateCompleted &&
			task.Status.State != protocol.TaskStateFailed &&
			task.Status.State != protocol.TaskStateCanceled {

			log.Infof("Task %s is %s, fetching final state...", task.ID, task.Status.State)

			queryParams := protocol.TaskQueryParams{
				ID: task.ID,
			}

			time.Sleep(500 * time.Millisecond)

			finalTask, err := a2aClient.GetTasks(ctx, queryParams)
			if err != nil {
				log.Fatalf("Failed to get task status: %v", err)
			}

			log.Infof("Task %s final state: %s", finalTask.ID, finalTask.Status.State)
			printTaskResult(finalTask)
		} else {
			printTaskResult(task)
		}
	} else {
		log.Infof("Received empty result")
	}
}

// getEventDescription returns a human-readable description of the streaming event
func getEventDescription(event protocol.StreamResponse) string {
	if msg := event.Message; msg != nil {
		ctxID := "unknown"
		if msg.ContextID != nil {
			ctxID = *msg.ContextID
		}
		return fmt.Sprintf("Message from %s, ContextID: %v", msg.Role, ctxID)
	}
	if task := event.Task; task != nil {
		return fmt.Sprintf("Task %s - State: %s, ContextID: %v", task.ID, task.Status.State, task.ContextID)
	}
	if su := event.StatusUpdate; su != nil {
		return fmt.Sprintf("Status Update - Task: %s, State: %s, ContextID: %v", su.TaskID, su.Status.State, su.ContextID)
	}
	if au := event.ArtifactUpdate; au != nil {
		artifactName := "Unnamed"
		if au.Artifact.Name != nil {
			artifactName = *au.Artifact.Name
		}
		return fmt.Sprintf("Artifact Update - %s, ContextID: %v", artifactName, au.ContextID)
	}
	return "Unknown event"
}

// extractFinalResult extracts the final text result from streaming events
func extractFinalResult(event protocol.StreamResponse) string {
	if msg := event.Message; msg != nil {
		for _, part := range msg.Parts {
			if t := part.TextContent(); t != "" {
				return t
			}
		}
	}
	if task := event.Task; task != nil {
		if task.Status.Message != nil {
			for _, part := range task.Status.Message.Parts {
				if t := part.TextContent(); t != "" {
					return t
				}
			}
		}
	}
	if au := event.ArtifactUpdate; au != nil {
		for _, part := range au.Artifact.Parts {
			if t := part.TextContent(); t != "" {
				return t
			}
		}
	}
	return ""
}

func printPartContent(prefix string, i int, part *protocol.Part) {
	switch c := part.Content.(type) {
	case protocol.Text:
		log.Infof("%sPart %d (text): %s", prefix, i+1, string(c))
	case protocol.URL:
		log.Infof("%sPart %d (url): %s", prefix, i+1, string(c))
	case protocol.Data:
		log.Infof("%sPart %d (data): %+v", prefix, i+1, c.Value)
	default:
		log.Infof("%sPart %d (unknown): %+v", prefix, i+1, part)
	}
}

// printMessage prints the contents of a message.
func printMessage(message protocol.Message) {
	log.Infof("Message ID: %s", message.MessageID)
	if message.ContextID != nil {
		log.Infof("Context ID: %s", *message.ContextID)
	}
	log.Infof("Role: %s", message.Role)

	log.Infof("Message parts:")
	for i, part := range message.Parts {
		printPartContent("  ", i, part)
	}
}

// printTaskResult prints the contents of a task result.
func printTaskResult(task *protocol.Task) {
	if task.Status.Message != nil {
		log.Infof("Task result message:")
		printMessage(*task.Status.Message)
	}

	if len(task.Artifacts) > 0 {
		log.Infof("Task artifacts:")
		for i, artifact := range task.Artifacts {
			name := "Unnamed"
			if artifact.Name != nil {
				name = *artifact.Name
			}
			log.Infof("  Artifact %d: %s", i+1, name)
			for j, part := range artifact.Parts {
				printPartContent("    ", j, part)
			}
		}
	}
}

// boolPtr returns a pointer to a boolean value.
func boolPtr(b bool) *bool {
	return &b
}
