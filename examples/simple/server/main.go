// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a simple A2A server example.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// simpleTaskProcessor implements the taskmanager.TaskProcessor interface.
type simpleTaskProcessor struct{}

// Process implements the taskmanager.TaskProcessor interface.
func (p *simpleTaskProcessor) Process(
	ctx context.Context,
	taskID string,
	message protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	// Extract text from the incoming message.
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text."
		log.Printf("Task %s failed: %s", taskID, errMsg)

		// Update status to Failed via handle.
		failedMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		_ = handle.UpdateStatus(protocol.TaskStateFailed, &failedMessage)
		return fmt.Errorf(errMsg)
	}

	log.Printf("Processing task %s with input: %s", taskID, text)

	// Process the input text (in this simple example, we'll just reverse it).
	result := reverseString(text)

	// Create response message.
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(fmt.Sprintf("Processed result: %s", result))},
	)

	// Update task status to completed.
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, &responseMessage); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	// Add the processed text as an artifact.
	artifact := protocol.Artifact{
		Name:        stringPtr("Reversed Text"),
		Description: stringPtr("The input text reversed"),
		Index:       0,
		Parts:       []protocol.Part{protocol.NewTextPart(result)},
		LastChunk:   boolPtr(true),
	}

	if err := handle.AddArtifact(artifact); err != nil {
		log.Printf("Error adding artifact for task %s: %v", taskID, err)
	}

	return nil
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// reverseString reverses a string.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// Helper function to create string pointers.
func stringPtr(s string) *string {
	return &s
}

// Helper function to create bool pointers.
func boolPtr(b bool) *bool {
	return &b
}

func main() {
	// Parse command-line flags.
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	// Create the agent card.
	agentCard := server.AgentCard{
		Name:        "Simple A2A Example Server",
		Description: stringPtr("A simple example A2A server that reverses text"),
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Name: "tRPC-A2A-Go Example",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              false,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{string(protocol.PartTypeText)},
		DefaultOutputModes: []string{string(protocol.PartTypeText)},
		Skills: []server.AgentSkill{
			{
				ID:          "text_reversal",
				Name:        "Text Reversal",
				Description: stringPtr("Reverses the input text"),
				Tags:        []string{"text", "processing"},
				Examples:    []string{"Hello, world!"},
				InputModes:  []string{string(protocol.PartTypeText)},
				OutputModes: []string{string(protocol.PartTypeText)},
			},
		},
	}

	// Create the task processor.
	processor := &simpleTaskProcessor{}

	// Create task manager and inject processor.
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create the server.
	srv, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Set up a channel to listen for termination signals.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a goroutine.
	go func() {
		serverAddr := fmt.Sprintf("%s:%d", *host, *port)
		log.Printf("Starting server on %s...", serverAddr)
		if err := srv.Start(serverAddr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for termination signal.
	sig := <-sigChan
	log.Printf("Received signal %v, shutting down...", sig)
}
