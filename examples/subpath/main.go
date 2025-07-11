// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates A2A agent with subpath configuration.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// simpleProcessor implements the MessageProcessor interface.
type simpleProcessor struct{}

func (p *simpleProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	taskHandler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Simply return a text response
	response := &protocol.Message{
		Role:      protocol.MessageRoleAgent,
		Kind:      protocol.KindMessage,
		MessageID: protocol.GenerateMessageID(),
		Parts: []protocol.Part{
			&protocol.TextPart{
				Kind: protocol.KindText,
				Text: "Hello from subpath agent!",
			},
		},
	}

	return &taskmanager.MessageProcessingResult{
		Result: response,
	}, nil
}

func main() {
	// Create agent card with subpath URL
	agentCard := server.AgentCard{
		Name:        "Subpath Agent",
		Description: "Demo agent with subpath",
		URL:         "http://localhost:8080/api/v1/agent", // Path will be extracted
		Version:     "1.0.0",
	}

	// Create task manager with simple processor
	taskManager, err := taskmanager.NewMemoryTaskManager(&simpleProcessor{})
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	a2aServer, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		log.Printf("Server starting on :8080")
		log.Printf("Endpoints available at:")
		log.Printf("  Agent Card: http://localhost:8080/api/v1/agent/.well-known/agent.json")
		log.Printf("  JSON-RPC: http://localhost:8080/api/v1/agent/")
		if err := a2aServer.Start(":8080"); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("Shutting down...")
	a2aServer.Stop(context.Background())
}
