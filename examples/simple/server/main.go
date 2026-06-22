// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
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

	goredis "github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/redis"
)

// simpleMessageProcessor implements the taskmanager.MessageProcessor interface.
type simpleMessageProcessor struct{}

// ProcessMessage implements the taskmanager.MessageProcessor interface.
func (p *simpleMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text."
		log.Errorf("Message processing failed: %s", errMsg)

		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]*protocol.Part{protocol.NewTextPart(errMsg)},
		)

		return &taskmanager.MessageProcessingResult{
			Result: protocol.NewSendMessageResponseMessage(&errorMessage),
		}, nil
	}

	log.Infof("Processing message with input: %s", text)

	result := reverseString(text)

	if !options.Streaming {
		responseMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]*protocol.Part{protocol.NewTextPart(fmt.Sprintf("Processed result: %s", result))},
		)

		return &taskmanager.MessageProcessingResult{
			Result: protocol.NewSendMessageResponseMessage(&responseMessage),
		}, nil
	}

	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}

	subscriber, err := handler.SubscribeTask(&taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to task: %w", err)
	}

	go func() {
		defer func() {
			if subscriber != nil {
				subscriber.Close()
			}
			handler.CleanTask(&taskID)
		}()

		startMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]*protocol.Part{protocol.NewTextPart("Task started, processing...")},
		)
		err = subscriber.Send(protocol.NewStreamResponseMessage(&startMessage))
		if err != nil {
			log.Errorf("Failed to send start message: %v", err)
		}

		err := subscriber.Send(protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: taskID,
			Status: protocol.TaskStatus{
				State: protocol.TaskStateWorking,
			},
		}))
		if err != nil {
			log.Errorf("Failed to send working event: %v", err)
		}

		responseMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]*protocol.Part{protocol.NewTextPart(fmt.Sprintf("Processed result: %s", result))},
		)

		err = subscriber.Send(protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
			TaskID: taskID,
			Status: protocol.TaskStatus{
				State:   protocol.TaskStateCompleted,
				Message: &responseMessage,
			},
			Final: true,
		}))
		if err != nil {
			log.Errorf("Failed to send completed event: %v", err)
		}

		artifact := protocol.Artifact{
			ArtifactID:  uuid.New().String(),
			Name:        stringPtr("Reversed Text"),
			Description: stringPtr("The input text reversed"),
			Parts:       []*protocol.Part{protocol.NewTextPart(result)},
		}

		err = subscriber.Send(protocol.NewStreamResponseArtifactUpdate(&protocol.TaskArtifactUpdateEvent{
			TaskID:    taskID,
			Artifact:  artifact,
			LastChunk: boolPtr(true),
		}))
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
		if t := part.TextContent(); t != "" {
			return t
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
	manager := flag.String("manager", "memory", "Task manager to use: memory or redis")
	flag.Parse()

	// Create the agent card.
	agentCard := server.AgentCard{
		Name:        "Simple A2A Example Server",
		Description: "A simple example A2A server that reverses text",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
			URL:          stringPtr(fmt.Sprintf("http://%s:%d/", *host, *port)),
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
	var taskManager taskmanager.TaskManager
	var err error
	switch *manager {
	case "memory":
		log.Infof("Using memory task manager")
		taskManager, err = memory.NewTaskManager(processor)
	case "redis":
		log.Infof("Using redis task manager")
		cli := goredis.NewClient(&goredis.Options{
			Addr: "localhost:6379",
		})
		taskManager, err = redis.NewTaskManager(cli, processor)
	default:
		log.Fatalf("Invalid task manager: %s", *manager)
	}

	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create the server.
	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
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
