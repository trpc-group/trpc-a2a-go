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
	"fmt"
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

	// Create a new unique task ID.
	taskID := uuid.New().String()

	// Create a new session ID.
	sessionID := uuid.New().String()
	log.Printf("Session ID: %s", sessionID)

	// Create the message to send.
	userMessage := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(*message)},
	)

	// Create task parameters.
	params := protocol.SendTaskParams{
		ID:        taskID,
		SessionID: &sessionID,
		Message:   userMessage,
	}

	log.Printf("Sending task %s with message: %s", taskID, *message)

	// Send task to the agent.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	task, err := a2aClient.SendTasks(ctx, params)
	if err != nil {
		log.Fatalf("Failed to send task: %v", err)
	}

	// Display the initial task response.
	log.Printf("Task %s initial state: %s", taskID, task.Status.State)

	// Wait for the task to complete if it's not already done.
	if task.Status.State != protocol.TaskStateCompleted && 
	   task.Status.State != protocol.TaskStateFailed && 
	   task.Status.State != protocol.TaskStateCanceled {
		
		log.Printf("Task %s is %s, fetching final state...", taskID, task.Status.State)
		
		// Get the task's final state.
		queryParams := protocol.TaskQueryParams{
			ID: taskID,
		}
		
		// Give the server some time to process.
		time.Sleep(500 * time.Millisecond)
		
		task, err = a2aClient.GetTasks(ctx, queryParams)
		if err != nil {
			log.Fatalf("Failed to get task status: %v", err)
		}
	}

	// Display the final task state.
	log.Printf("Task %s final state: %s", taskID, task.Status.State)

	// Display the response message if available.
	if task.Status.Message != nil {
		fmt.Println("\nAgent response:")
		for _, part := range task.Status.Message.Parts {
			if textPart, ok := part.(protocol.TextPart); ok {
				fmt.Println(textPart.Text)
			}
		}
	}

	// Display any artifacts.
	if len(task.Artifacts) > 0 {
		fmt.Println("\nArtifacts:")
		for i, artifact := range task.Artifacts {
			// Display artifact name and description if available.
			if artifact.Name != nil {
				fmt.Printf("%d. %s", i+1, *artifact.Name)
				if artifact.Description != nil {
					fmt.Printf(" - %s", *artifact.Description)
				}
				fmt.Println()
			} else {
				fmt.Printf("%d. Artifact #%d\n", i+1, i+1)
			}

			// Display artifact content.
			for _, part := range artifact.Parts {
				if textPart, ok := part.(protocol.TextPart); ok {
					fmt.Printf("   %s\n", textPart.Text)
				}
			}
		}
	}
} 