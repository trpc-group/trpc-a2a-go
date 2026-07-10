// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a streaming A2A server example. The processing logic
// is a mock agent (examples/util): it splits the input into chunks, reverses
// each, and emits them with a delay to simulate real-time streaming.
// util.NewMessageProcessor maps that event stream onto the task lifecycle, so
// the same code serves message/send (which waits for the final snapshot) and
// message/stream (which relays every chunk as it happens).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/examples/util"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// newAgent builds the mock agent backing this server: it splits the input into
// short chunks, reverses each, and streams them ~500ms apart.
func newAgent() util.Agent {
	return util.NewMockAgent(
		func(input string) []string {
			chunks := util.Chunk(input, 5)
			for i, c := range chunks {
				chunks[i] = reverseString(c)
			}
			return chunks
		},
		util.WithChunkDelay(500*time.Millisecond),
	)
}

// reverseString reverses a UTF-8 encoded string.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func main() {
	// Command-line flags for server configuration
	var (
		host string
		port int
	)

	flag.StringVar(&host, "host", "localhost", "Server host address")
	flag.IntVar(&port, "port", 8089, "Server port")
	flag.Parse()

	address := fmt.Sprintf("%s:%d", host, port)
	serverURL := fmt.Sprintf("http://%s/", address)

	// Create the agent card
	agentCard := server.AgentCard{
		Name:        "Streaming Text Processor",
		Description: "A2A streaming example server that processes text in chunks",
		URL:         serverURL,
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-go Examples",
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
				ID:          "streaming_processor",
				Name:        "Streaming Text Processor",
				Description: stringPtr("Input: Any text\nOutput: The text split into chunks, each reversed, delivered incrementally"),
				Tags:        []string{"text", "stream", "example"},
				Examples: []string{
					"The quick brown fox jumps over the lazy dog",
					"Lorem ipsum dolor sit amet",
					"This demonstrates streaming capabilities",
				},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// Adapt the mock agent into a MessageProcessor and inject it into the TaskManager.
	taskManager, err := memory.NewTaskManager(util.NewMessageProcessor(newAgent()))
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create the A2A server instance
	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start the server in a goroutine
	go func() {
		log.Infof("Starting streaming server on %s...", address)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sig := <-sigChan
	log.Infof("Received signal %v, shutting down server...", sig)

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		log.Fatalf("Error during server shutdown: %v", err)
	}

	log.Infof("Server shutdown complete")
}

// Helper functions to create pointers
func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}
