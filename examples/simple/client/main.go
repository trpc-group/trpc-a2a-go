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
		[]protocol.Part{protocol.NewTextPart(*message)},
	)

	// Create message parameters using the new SendMessageParams structure.
	params := protocol.SendMessageParams{
		Message: userMessage,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking:            boolPtr(false), // Non-blocking for streaming, blocking for standard
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

	// Handle the result based on its type
	switch result := messageResult.Result.(type) {
	case *protocol.Message:
		log.Infof("Received message response:")
		printMessage(*result)
	case *protocol.Task:
		log.Infof("Received task response - ID: %s, State: %s", result.ID, result.Status.State)

		// If task is not completed, wait and check again
		if result.Status.State != protocol.TaskStateCompleted &&
			result.Status.State != protocol.TaskStateFailed &&
			result.Status.State != protocol.TaskStateCanceled {

			log.Infof("Task %s is %s, fetching final state...", result.ID, result.Status.State)

			// Get the task's final state.
			queryParams := protocol.TaskQueryParams{
				ID: result.ID,
			}

			// Give the server some time to process.
			time.Sleep(500 * time.Millisecond)

			task, err := a2aClient.GetTasks(ctx, queryParams)
			if err != nil {
				log.Fatalf("Failed to get task status: %v", err)
			}

			log.Infof("Task %s final state: %s", task.ID, task.Status.State)
			printTaskResult(task)
		} else {
			printTaskResult(result)
		}
	default:
		log.Infof("Received unknown result type: %T", result)
	}
}

// getEventDescription returns a human-readable description of the streaming event
func getEventDescription(event protocol.StreamingMessageEvent) string {
	switch result := event.Result.(type) {
	case *protocol.Message:
		ctxID := "unknown"
		if result.ContextID != nil {
			ctxID = *result.ContextID
		}
		return fmt.Sprintf("Message from %s, ContextID: %v", result.Role, ctxID)
	case *protocol.Task:
		return fmt.Sprintf("Task %s - State: %s, ContextID: %v", result.ID, result.Status.State, result.ContextID)
	case *protocol.TaskStatusUpdateEvent:
		return fmt.Sprintf(
			"Status Update - Task: %s, State: %s, ContextID: %v",
			result.TaskID,
			result.Status.State,
			result.ContextID,
		)
	case *protocol.TaskArtifactUpdateEvent:
		artifactName := "Unnamed"
		if result.Artifact.Name != nil {
			artifactName = *result.Artifact.Name
		}
		return fmt.Sprintf("Artifact Update - %s, ContextID: %v", artifactName, result.ContextID)
	default:
		return fmt.Sprintf("Unknown event type: %T", result)
	}
}

// extractFinalResult extracts the final text result from streaming events
func extractFinalResult(event protocol.StreamingMessageEvent) string {
	switch result := event.Result.(type) {
	case *protocol.Message:
		// Extract text from message parts
		for _, part := range result.Parts {
			if textPart, ok := part.(*protocol.TextPart); ok {
				return textPart.Text
			}
		}
	case *protocol.Task:
		// Extract text from task status message
		if result.Status.Message != nil {
			for _, part := range result.Status.Message.Parts {
				if textPart, ok := part.(*protocol.TextPart); ok {
					return textPart.Text
				}
			}
		}
	case *protocol.TaskArtifactUpdateEvent:
		// Extract text from artifact parts
		for _, part := range result.Artifact.Parts {
			if textPart, ok := part.(*protocol.TextPart); ok {
				return textPart.Text
			}
		}
	}
	return ""
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
		switch p := part.(type) {
		case *protocol.TextPart:
			log.Infof("  Part %d (text): %s", i+1, p.Text)
		case *protocol.FilePart:
			log.Infof("  Part %d (file): [file content]", i+1)
		case *protocol.DataPart:
			log.Infof("  Part %d (data): %+v", i+1, p.Data)
		default:
			log.Infof("  Part %d (unknown): %+v", i+1, part)
		}
	}
}

// printTaskResult prints the contents of a task result.
func printTaskResult(task *protocol.Task) {
	if task.Status.Message != nil {
		log.Infof("Task result message:")
		printMessage(*task.Status.Message)
	}

	// Print artifacts if any
	if len(task.Artifacts) > 0 {
		log.Infof("Task artifacts:")
		for i, artifact := range task.Artifacts {
			name := "Unnamed"
			if artifact.Name != nil {
				name = *artifact.Name
			}
			log.Infof("  Artifact %d: %s", i+1, name)
			for j, part := range artifact.Parts {
				switch p := part.(type) {
				case *protocol.TextPart:
					log.Infof("    Part %d (text): %s", j+1, p.Text)
				case *protocol.FilePart:
					log.Infof("    Part %d (file): [file content]", j+1)
				case *protocol.DataPart:
					log.Infof("    Part %d (data): %+v", j+1, p.Data)
				default:
					log.Infof("    Part %d (unknown): %+v", j+1, part)
				}
			}
		}
	}
}

// boolPtr returns a pointer to a boolean value.
func boolPtr(b bool) *bool {
	return &b
}
