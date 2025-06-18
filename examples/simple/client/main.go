// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a simple A2A client example.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/google/uuid"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func main() {
	// Parse command-line flags.
	agentURL := flag.String("agent", "http://localhost:8080/", "Target A2A agent URL")
	timeout := flag.Duration("timeout", 30*time.Second, "Request timeout (e.g., 30s, 1m)")
	message := flag.String("message", "Hello, world!", "Message to send to the agent")
	flag.Parse()

	// Create A2A client.
	a2aClient, err := client.NewA2AClient(*agentURL, client.WithTimeout(*timeout))
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	// Display connection information.
	log.Printf("Connecting to agent: %s (Timeout: %v)", *agentURL, *timeout)

	// Create a new unique message ID and context ID.
	contextID := uuid.New().String()
	log.Printf("Context ID: %s", contextID)

	// Create the message to send using the new constructor.
	userMessage := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(*message)},
		nil, // taskID
		&contextID,
	)

	// Create message parameters using the new SendMessageParams structure.
	params := protocol.SendMessageParams{
		Message: userMessage,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(true), // Wait for completion
		},
	}

	log.Printf("Sending message with content: %s", *message)

	// Send message to the agent using the new message API.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	messageResult, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		log.Fatalf("Failed to send message: %v", err)
	}

	// Display the result.
	log.Printf("Message sent successfully")

	// Handle the result based on its type
	switch result := messageResult.Result.(type) {
	case *protocol.Message:
		log.Printf("Received message response:")
		printMessage(*result)
	case *protocol.Task:
		log.Printf("Received task response - ID: %s, State: %s", result.ID, result.Status.State)

		// If task is not completed, wait and check again
		if result.Status.State != protocol.TaskStateCompleted &&
			result.Status.State != protocol.TaskStateFailed &&
			result.Status.State != protocol.TaskStateCanceled {

			log.Printf("Task %s is %s, fetching final state...", result.ID, result.Status.State)

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

			log.Printf("Task %s final state: %s", task.ID, task.Status.State)
			printTaskResult(task)
		} else {
			printTaskResult(result)
		}
	default:
		log.Printf("Received unknown result type: %T", result)
	}
}

// printMessage prints the contents of a message.
func printMessage(message protocol.Message) {
	log.Printf("Message ID: %s", message.MessageID)
	if message.ContextID != nil {
		log.Printf("Context ID: %s", *message.ContextID)
	}
	log.Printf("Role: %s", message.Role)

	log.Printf("Message parts:")
	for i, part := range message.Parts {
		switch p := part.(type) {
		case *protocol.TextPart:
			log.Printf("  Part %d (text): %s", i+1, p.Text)
		case *protocol.FilePart:
			log.Printf("  Part %d (file): [file content]", i+1)
		case *protocol.DataPart:
			log.Printf("  Part %d (data): %+v", i+1, p.Data)
		default:
			log.Printf("  Part %d (unknown): %+v", i+1, part)
		}
	}
}

// printTaskResult prints the contents of a task result.
func printTaskResult(task *protocol.Task) {
	if task.Status.Message != nil {
		log.Printf("Task result message:")
		printMessage(*task.Status.Message)
	}

	// Print artifacts if any
	if len(task.Artifacts) > 0 {
		log.Printf("Task artifacts:")
		for i, artifact := range task.Artifacts {
			name := "Unnamed"
			if artifact.Name != nil {
				name = *artifact.Name
			}
			log.Printf("  Artifact %d: %s", i+1, name)
			for j, part := range artifact.Parts {
				switch p := part.(type) {
				case *protocol.TextPart:
					log.Printf("    Part %d (text): %s", j+1, p.Text)
				case *protocol.FilePart:
					log.Printf("    Part %d (file): [file content]", j+1)
				case *protocol.DataPart:
					log.Printf("    Part %d (data): %+v", j+1, p.Data)
				default:
					log.Printf("    Part %d (unknown): %+v", j+1, part)
				}
			}
		}
	}
}

// boolPtr returns a pointer to a boolean value.
func boolPtr(b bool) *bool {
	return &b
}
