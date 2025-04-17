// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main provides a simple A2A server using the Redis task manager.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
	redismgr "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
)

// DemoTaskProcessor implements TaskProcessor for our demo server.
type DemoTaskProcessor struct{}

// Process implements the task processing logic.
func (p *DemoTaskProcessor) Process(
	ctx context.Context,
	taskID string,
	initialMsg protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	// First, update the status to show we're working
	if err := handle.UpdateStatus(protocol.TaskStateWorking, nil); err != nil {
		return fmt.Errorf("failed to update status to working: %w", err)
	}

	// Extract the user message
	var userMessage string
	if len(initialMsg.Parts) > 0 {
		if textPart, ok := initialMsg.Parts[0].(protocol.TextPart); ok {
			userMessage = textPart.Text
		}
	}

	// Log the task
	log.Printf("Processing task %s: %s", taskID, userMessage)

	// Simulate some work
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1 * time.Second):
		// Processed
	}

	// Create a response based on the user message
	response := fmt.Sprintf("Processed: %s", userMessage)
	if strings.Contains(strings.ToLower(userMessage), "error") {
		return fmt.Errorf("simulated error requested in message")
	}

	// Add an artifact
	artifact := protocol.Artifact{
		Name:  strPtr("result"),
		Parts: []protocol.Part{protocol.NewTextPart(response)},
		Index: 0,
	}
	lastChunk := true
	artifact.LastChunk = &lastChunk

	if err := handle.AddArtifact(artifact); err != nil {
		return fmt.Errorf("failed to add artifact: %w", err)
	}

	// Complete with a success message
	successMsg := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Task completed: %s", userMessage)),
		},
	}
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, successMsg); err != nil {
		return fmt.Errorf("failed to update final status: %w", err)
	}

	return nil
}

// Helper function to create string pointers
func strPtr(s string) *string {
	return &s
}

func main() {
	// Parse command line flags
	port := flag.Int("port", 8080, "Server port")
	redisAddr := flag.String("redis", "localhost:6379", "Redis server address")
	redisPassword := flag.String("redis-pass", "", "Redis password")
	redisDB := flag.Int("redis-db", 0, "Redis database")
	flag.Parse()

	log.Printf("Starting A2A server with Redis task manager on port %d", *port)
	log.Printf("Using Redis at %s (DB: %d)", *redisAddr, *redisDB)

	// Create a task processor
	processor := &DemoTaskProcessor{}

	// Configure Redis client
	redisOptions := &redis.UniversalOptions{
		Addrs:    []string{*redisAddr},
		Password: *redisPassword,
		DB:       *redisDB,
	}

	// Create Redis task manager
	manager, err := redismgr.NewRedisTaskManager(
		redis.NewUniversalClient(redisOptions),
		processor,
	)
	if err != nil {
		log.Fatalf("Failed to create Redis task manager: %v", err)
	}
	defer manager.Close()

	// Define the agent card with server metadata
	description := "A simple A2A demo server using Redis task manager"
	serverURL := fmt.Sprintf("http://localhost:%d", *port)
	version := "1.0.0"

	agentCard := server.AgentCard{
		Name:        "Redis Task Manager Demo",
		Description: &description,
		URL:         serverURL,
		Version:     version,
		Capabilities: server.AgentCapabilities{
			Streaming:              true,
			PushNotifications:      false,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{string(protocol.PartTypeText)},
		DefaultOutputModes: []string{string(protocol.PartTypeText)},
		Skills:             []server.AgentSkill{}, // No specific skills
	}

	// Create A2A server using the official server package
	a2aServer, err := server.NewA2AServer(agentCard, manager, server.WithCORSEnabled(true))
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	a2aServer.Start(fmt.Sprintf(":%d", *port))
}
