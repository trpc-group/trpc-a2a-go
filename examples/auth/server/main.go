// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements an A2A server with authentication and push notification authentication.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

func main() {
	// Create a simple echo processor for demonstration purposes
	processor := &echoProcessor{}

	// Create a real task manager with our processor
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Define JWT authentication secret
	// In a real application, this should be securely stored and loaded from environment or config
	jwtSecret := make([]byte, 32)
	if _, err := rand.Read(jwtSecret); err != nil {
		log.Fatalf("Failed to generate JWT secret: %v", err)
	}

	// Create JWT auth provider
	jwtProvider := auth.NewJWTAuthProvider(
		jwtSecret,
		"a2a-server", // audience
		"example",    // issuer
		1*time.Hour,  // token lifetime
	)

	// Create API key auth provider
	// In a real application, these would come from a database or configuration
	apiKeys := map[string]string{
		"test-api-key": "test-user",
	}
	apiKeyProvider := auth.NewAPIKeyAuthProvider(apiKeys, "X-API-Key")

	// Chain the auth providers to support both JWT and API key authentication
	chainProvider := auth.NewChainAuthProvider(jwtProvider, apiKeyProvider)

	// Create agent card with authentication info
	agentCard := server.AgentCard{
		Name:        "A2A Server with Authentication",
		Description: addressableStr("A demonstration server with JWT and API key authentication"),
		URL:         "http://localhost:8080",
		Provider: &server.AgentProvider{
			Name: "Example Provider",
		},
		Version: "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
		},
		Authentication: &server.AgentAuthentication{
			Type:     "apiKey,jwt",
			Required: true,
			Config: map[string]interface{}{
				"jwt": map[string]interface{}{
					"audience": "a2a-server",
					"issuer":   "example",
				},
				"apiKey": map[string]interface{}{
					"headerName": "X-API-Key",
				},
			},
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	// Create the server with authentication
	a2aServer, err := server.NewA2AServer(
		agentCard,
		taskManager,
		server.WithAuthProvider(chainProvider),
		server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, initiating shutdown...", sig)
		cancel()
	}()

	// Start the server in a goroutine
	go func() {
		addr := "localhost:8080"
		log.Printf("Starting A2A server on %s...", addr)
		if err := a2aServer.Start(addr); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Create a token for testing
	token, err := jwtProvider.CreateToken("test-user", nil)
	if err != nil {
		log.Printf("Warning: Failed to create test token: %v", err)
	} else {
		log.Printf("Test JWT token: %s", token)
		log.Printf("Example curl command:")
		log.Printf("curl -X POST http://localhost:8080 -H 'Content-Type: application/json' "+
			"-H 'Authorization: Bearer %s' "+
			"-d '{\"jsonrpc\":\"2.0\",\"method\":\"tasks/send\",\"id\":1,"+
			"\"params\":{\"id\":\"task1\",\"message\":{\"role\":\"user\","+
			"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'", token)
		log.Printf("Or use API key:")
		log.Printf("curl -X POST http://localhost:8080 -H 'Content-Type: application/json' " +
			"-H 'X-API-Key: test-api-key' " +
			"-d '{\"jsonrpc\":\"2.0\",\"method\":\"tasks/send\",\"id\":1," +
			"\"params\":{\"id\":\"task1\",\"message\":{\"role\":\"user\"," +
			"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'")
	}

	// Wait for context cancellation (from signal handler)
	<-ctx.Done()

	// Perform graceful shutdown with a 5-second timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(), 5*time.Second,
	)
	defer shutdownCancel()
	if err := a2aServer.Stop(shutdownCtx); err != nil {
		log.Printf("Error during server shutdown: %v", err)
	}
	log.Println("Server shutdown complete")
}

// echoProcessor is a simple processor that echoes user messages
type echoProcessor struct{}

func (p *echoProcessor) Process(
	ctx context.Context,
	taskID string,
	msg protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	// Create a concatenated string of all text parts
	var responseText string
	for _, part := range msg.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			responseText += textPart.Text + " "
		}
	}

	// Create response message
	responseMsg := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Echo: %s", responseText)),
		},
	}

	// Update the task status to completed with our response
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, responseMsg); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	return nil
}

func addressableStr(s string) *string {
	return &s
}
