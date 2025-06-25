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
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// simpleMessageProcessor implements the taskmanager.MessageProcessor interface.
type simpleMessageProcessor struct{}

// ProcessMessage implements the taskmanager.MessageProcessor interface.
func (p *simpleMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Extract text from the incoming message.
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text."
		log.Errorf("Message processing failed: %s", errMsg)

		// Return error message directly
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)

		return &taskmanager.MessageProcessingResult{
			Result: &errorMessage,
		}, nil
	}

	log.Infof("Processing message with input: %s", text)

	// Process the input text (in this simple example, we'll just reverse it).
	result := reverseString(text)

	// For non-streaming processing, we can return either a Message or Task
	if !options.Streaming {
		// Return a direct message response
		responseMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(fmt.Sprintf("Processed result: %s", result))},
		)

		return &taskmanager.MessageProcessingResult{
			Result: &responseMessage,
		}, nil
	}

	// For streaming processing, create a task and subscribe to it
	taskID, err := handle.BuildTask(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}

	// Subscribe to the task for streaming events
	subscriber, err := handle.SubScribeTask(&taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to task: %w", err)
	}

	// Start processing in a goroutine
	go func() {
		defer func() {
			if subscriber != nil {
				subscriber.Close()
			}
		}()

		// Send task status update - working
		workingEvent := protocol.StreamingMessageEvent{
			Result: &protocol.TaskStatusUpdateEvent{
				TaskID:    taskID,
				ContextID: "",
				Kind:      "status-update",
				Status: protocol.TaskStatus{
					State: protocol.TaskStateWorking,
				},
			},
		}
		err := subscriber.Send(workingEvent)
		if err != nil {
			log.Errorf("Failed to send working event: %v", err)
		}

		// Create response message
		responseMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(fmt.Sprintf("Processed result: %s", result))},
		)

		// Send task completion
		completedEvent := protocol.StreamingMessageEvent{
			Result: &protocol.TaskStatusUpdateEvent{
				TaskID:    taskID,
				ContextID: "",
				Kind:      "status-update",
				Status: protocol.TaskStatus{
					State:   protocol.TaskStateCompleted,
					Message: &responseMessage,
				},
				Final: true,
			},
		}
		err = subscriber.Send(completedEvent)
		if err != nil {
			log.Errorf("Failed to send completed event: %v", err)
		}

		// Add artifact
		artifact := protocol.Artifact{
			ArtifactID:  uuid.New().String(),
			Name:        stringPtr("Reversed Text"),
			Description: stringPtr("The input text reversed"),
			Parts:       []protocol.Part{protocol.NewTextPart(result)},
		}

		artifactEvent := protocol.StreamingMessageEvent{
			Result: &protocol.TaskArtifactUpdateEvent{
				TaskID:    taskID,
				ContextID: "",
				Kind:      "artifact-update",
				Artifact:  artifact,
				LastChunk: boolPtr(true),
			},
		}
		err = subscriber.Send(artifactEvent)
		if err != nil {
			log.Errorf("Failed to send artifact event: %v", err)
		}
	}()

	return &taskmanager.MessageProcessingResult{
		StreamingEvents: subscriber,
	}, nil
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
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
		Description: "A simple example A2A server that reverses text",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_reversal",
				Name:        "Text Reversal",
				Description: stringPtr("Reverses the input text"),
				Tags:        []string{"text", "processing"},
				Examples:    []string{"Hello, world!"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// Create the message processor.
	processor := &simpleMessageProcessor{}

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
		log.Infof("Starting server on %s...", serverAddr)
		if err := srv.Start(serverAddr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for termination signal.
	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)
}
