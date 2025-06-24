// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates how to serve an A2A agent behind a sub-path.
// This example uses the new WithBasePath option for simple integration.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// simpleTextProcessor implements a basic message processor.
type simpleTextProcessor struct{}

func (p *simpleTextProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Extract text from the message.
	text := extractTextFromMessage(message)
	if text == "" {
		return nil, fmt.Errorf("no text found in message")
	}

	// Simple processing: reverse the text.
	result := reverseString(text)

	// Create response message.
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(fmt.Sprintf("Reversed: %s", result))},
	)

	return &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}, nil
}

func extractTextFromMessage(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// Helper functions for pointer conversion.
func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

// createAgentCard creates an agent card with the correct base URL.
func createAgentCard(baseURL string) server.AgentCard {
	return server.AgentCard{
		Name:        "SubPath Text Processor Agent",
		Description: "A simple agent that reverses text, served under a sub-path",
		URL:         baseURL,
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming: boolPtr(false),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_processing",
				Name:        "Text Processing",
				Description: stringPtr("Can reverse text strings"),
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}
}

func main() {
	var (
		port    = flag.String("port", "8080", "Port to listen on")
		subPath = flag.String("subPath", "/agent/api/v2/myagent", "Sub-path prefix for the agent")
		baseURL = flag.String("baseURL", "", "Base URL for the agent (auto-detected if empty)")
	)
	flag.Parse()

	// Auto-detect base URL if not provided.
	if *baseURL == "" {
		*baseURL = fmt.Sprintf("http://localhost:%s%s", *port, *subPath)
	}

	// Create the agent card with the correct base URL.
	agentCard := createAgentCard(*baseURL)

	// Create the message processor.
	processor := &simpleTextProcessor{}

	// Create the task manager.
	taskMgr, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create the A2A server with base path option.
	// This automatically configures all endpoints under the sub-path.
	a2aServer, err := server.NewA2AServer(agentCard, taskMgr,
		server.WithBasePath(*subPath))
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Create HTTP server with direct mounting.
	srv := &http.Server{
		Addr:    ":" + *port,
		Handler: a2aServer.Handler(),
	}

	// Print example URLs.
	printExampleURLs(*port, *subPath)

	// Setup graceful shutdown.
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}()

	// Start the server.
	log.Printf("Starting A2A agent server on :%s", *port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}

	log.Println("Server stopped")
}

func printExampleURLs(port, subPath string) {
	log.Println("\n" + strings.Repeat("=", 60))
	log.Printf("A2A Agent Server with Sub-Path")
	log.Println(strings.Repeat("=", 60))

	baseURL := fmt.Sprintf("http://localhost:%s", port)

	log.Println("\nA2A Agent endpoints:")
	log.Printf("  Agent card: %s%s/.well-known/agent.json", baseURL, subPath)
	log.Printf("  JSON-RPC:   %s%s/", baseURL, subPath)

	log.Println("\nExample usage:")
	fmt.Printf("  # Get agent metadata\n")
	fmt.Printf(`  curl "%s%s/.well-known/agent.json"`, baseURL, subPath)
	fmt.Printf("\n  # Send a message\n")
	fmt.Printf(`  curl -X POST "%s%s/" `, baseURL, subPath)
	fmt.Printf(" -H \"Content-Type: application/json\" ")
	fmt.Println(` -d '{"jsonrpc":"2.0","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hello"}]}},"id":"1"}'`)

	log.Println("\n" + strings.Repeat("=", 60) + "\n")
}
