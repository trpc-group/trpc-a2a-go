// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main provides a simple A2A client to interact with the server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// printTaskDetails prints the details of a task to the console.
func printTaskDetails(task *protocol.Task) {
	log.Printf("Task ID: %s", task.ID)
	log.Printf("State: %s", task.Status.State)
	log.Printf("Timestamp: %s", task.Status.Timestamp)
	log.Printf("Message history: %d items", len(task.History))

	if len(task.Artifacts) > 0 {
		log.Printf("Artifacts:")
		for i, artifact := range task.Artifacts {
			log.Printf("  [%d] %s", i, artifactToString(artifact))
		}
	}

	if task.Status.Message != nil {
		log.Printf("Status message: %s", messageToString(*task.Status.Message))
	}
}

// messageToString converts a message to a string representation.
func messageToString(msg protocol.Message) string {
	if len(msg.Parts) == 0 {
		return "[empty message]"
	}

	var result strings.Builder
	for _, part := range msg.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			result.WriteString(textPart.Text)
		} else {
			result.WriteString("[non-text part]")
		}
	}
	return result.String()
}

// artifactToString converts an artifact to a string representation.
func artifactToString(artifact protocol.Artifact) string {
	var parts []string
	for _, part := range artifact.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			parts = append(parts, textPart.Text)
		} else {
			parts = append(parts, "[non-text part]")
		}
	}

	name := "unnamed"
	if artifact.Name != nil {
		name = *artifact.Name
	}

	lastChunk := false
	if artifact.LastChunk != nil {
		lastChunk = *artifact.LastChunk
	}

	return fmt.Sprintf("%s (index: %d, last chunk: %v): %v",
		name, artifact.Index, lastChunk, parts)
}

// pollTaskUntilDone polls a task until it reaches a final state.
func pollTaskUntilDone(ctx context.Context, a2aClient *client.A2AClient, taskID string) (*protocol.Task, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			task, err := a2aClient.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})
			if err != nil {
				return nil, fmt.Errorf("failed to get task: %w", err)
			}

			// Check if the task is done
			if task.Status.State == protocol.TaskStateCompleted ||
				task.Status.State == protocol.TaskStateFailed ||
				task.Status.State == protocol.TaskStateCanceled {
				return task, nil
			}

			log.Printf("Task %s state: %s", taskID, task.Status.State)

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// generateTaskID creates a task ID if none is provided.
func generateTaskID(providedID string) string {
	if providedID != "" {
		return providedID
	}
	id := fmt.Sprintf("task-%d", time.Now().UnixNano())
	log.Printf("Generated task ID: %s", id)
	return id
}

// createSendTaskParams creates parameters for sending a task.
func createSendTaskParams(taskID, message, idempotencyKey string) protocol.SendTaskParams {
	params := protocol.SendTaskParams{
		ID: taskID,
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []protocol.Part{
				protocol.NewTextPart(message),
			},
		},
	}

	// Add idempotency key if provided
	if idempotencyKey != "" {
		if params.Metadata == nil {
			params.Metadata = make(map[string]interface{})
		}
		params.Metadata["idempotency_key"] = idempotencyKey
	}

	return params
}

// handleSendOperation handles the "send" operation.
func handleSendOperation(ctx context.Context, a2aClient *client.A2AClient, taskID, message, idempotencyKey string) {
	id := generateTaskID(taskID)
	sendParams := createSendTaskParams(id, message, idempotencyKey)

	// Send the task
	task, err := a2aClient.SendTasks(ctx, sendParams)
	if err != nil {
		log.Fatalf("Failed to send task: %v", err)
	}

	log.Printf("Task created: %s", task.ID)
	log.Printf("Initial state: %s", task.Status.State)

	// Poll for task completion
	log.Println("Polling for task completion...")
	finalTask, err := pollTaskUntilDone(ctx, a2aClient, task.ID)
	if err != nil {
		log.Fatalf("Failed to poll task: %v", err)
	}

	printFinalTaskStatus(finalTask)
}

// printFinalTaskStatus prints the final status of a completed task.
func printFinalTaskStatus(task *protocol.Task) {
	log.Printf("Task %s final state: %s", task.ID, task.Status.State)

	// Print the artifacts
	if len(task.Artifacts) > 0 {
		for i, artifact := range task.Artifacts {
			log.Printf("Artifact %d: %s", i, artifactToString(artifact))
		}
	}

	// Print the final message if present
	if task.Status.Message != nil {
		log.Printf("Final message: %s", messageToString(*task.Status.Message))
	}
}

// handleGetOperation handles the "get" operation.
func handleGetOperation(ctx context.Context, a2aClient *client.A2AClient, taskID string) {
	if taskID == "" {
		log.Fatalf("Task ID is required for get operation")
	}

	// Create query parameters
	params := protocol.TaskQueryParams{
		ID:            taskID,
		HistoryLength: nil, // Get default history length
	}

	// Get the task
	task, err := a2aClient.GetTasks(ctx, params)
	if err != nil {
		log.Fatalf("Failed to get task: %v", err)
	}

	// Print task details
	printTaskDetails(task)
}

// handleCancelOperation handles the "cancel" operation.
func handleCancelOperation(ctx context.Context, a2aClient *client.A2AClient, taskID string) {
	if taskID == "" {
		log.Fatalf("Task ID is required for cancel operation")
	}

	// Create cancel parameters
	params := protocol.TaskIDParams{
		ID: taskID,
	}

	// Cancel the task
	task, err := a2aClient.CancelTasks(ctx, params)
	if err != nil {
		log.Fatalf("Failed to cancel task: %v", err)
	}

	log.Printf("Task %s canceled, final state: %s", task.ID, task.Status.State)
}

// processStreamEvents processes events from the streaming API.
func processStreamEvents(eventChan <-chan protocol.TaskEvent) {
	for event := range eventChan {
		switch e := event.(type) {
		case protocol.TaskStatusUpdateEvent:
			handleStatusUpdateEvent(e)
		case protocol.TaskArtifactUpdateEvent:
			handleArtifactUpdateEvent(e)
		default:
			log.Printf("Unknown event type: %T", event)
		}
	}
}

// handleStatusUpdateEvent processes a status update event.
func handleStatusUpdateEvent(event protocol.TaskStatusUpdateEvent) {
	log.Printf("Status update: %s", event.Status.State)
	if event.Final {
		log.Printf("Final status: %s", event.Status.State)
		if event.Status.Message != nil {
			log.Printf("Final message: %s", messageToString(*event.Status.Message))
		}
	}
}

// handleArtifactUpdateEvent processes an artifact update event.
func handleArtifactUpdateEvent(event protocol.TaskArtifactUpdateEvent) {
	name := "unnamed"
	if event.Artifact.Name != nil {
		name = *event.Artifact.Name
	}
	log.Printf("Artifact: %s (Index: %d)", name, event.Artifact.Index)
	log.Printf("  Content: %s", artifactToString(event.Artifact))
	if event.Final {
		log.Printf("  (This is the final artifact)")
	}
}

// handleStreamOperation handles the "stream" operation.
func handleStreamOperation(ctx context.Context, a2aClient *client.A2AClient, taskID, message string) {
	id := generateTaskID(taskID)
	sendParams := createSendTaskParams(id, message, "")

	// Use the streaming API (SendTaskSubscribe)
	eventChan, err := a2aClient.StreamTask(ctx, sendParams)
	if err != nil {
		log.Fatalf("Failed to start streaming task: %v", err)
	}

	log.Printf("Started streaming task %s", sendParams.ID)

	// Process events
	processStreamEvents(eventChan)

	log.Printf("Streaming completed for task %s", sendParams.ID)
}

func main() {
	serverURL := flag.String("server", "http://localhost:8080", "A2A server URL")
	message := flag.String("message", "Hello, world!", "Message to send")
	operation := flag.String("op", "send", "Operation: send, get, cancel, or stream")
	taskID := flag.String("task", "", "Task ID (required for get and cancel operations)")
	idempotencyKey := flag.String("idkey", "", "Idempotency key for task creation (generated if not provided)")
	flag.Parse()

	// Create A2A client
	a2aClient, err := client.NewA2AClient(*serverURL)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}
	ctx := context.Background()

	switch *operation {
	case "send":
		handleSendOperation(ctx, a2aClient, *taskID, *message, *idempotencyKey)
	case "get":
		handleGetOperation(ctx, a2aClient, *taskID)
	case "cancel":
		handleCancelOperation(ctx, a2aClient, *taskID)
	case "stream":
		handleStreamOperation(ctx, a2aClient, *taskID, *message)
	default:
		log.Fatalf("Unknown operation: %s", *operation)
	}
}
