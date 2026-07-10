// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main provides a Redis TaskManager example server that demonstrates
// how to use the Redis-based task manager for processing text conversion tasks.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	redisTaskManager "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/redis"
)

const (
	// Default configuration values
	defaultRedisAddress  = "localhost:6379"
	defaultServerAddress = ":8080"

	// Processing step timing constants
	analysisDelay         = 500 * time.Millisecond
	processingDelay       = 700 * time.Millisecond
	conversionDelay       = 500 * time.Millisecond
	artifactCreationDelay = 300 * time.Millisecond

	// Processing step messages
	msgStarting   = "[STARTING] Initializing text conversion process..."
	msgAnalyzing  = "[ANALYZING] Processing input text (%d characters)..."
	msgProcessing = "[PROCESSING] Converting text to lowercase..."
	msgArtifact   = "[ARTIFACT] Creating result artifact..."
	msgCompleted  = "[COMPLETED] Text processing finished! Original: '%s' -> Lowercase: '%s'"

	// Server information
	serverName        = "Text Case Converter"
	serverDescription = "A simple agent that converts text to lowercase using Redis storage"
	serverVersion     = "1.0.0"
	organizationName  = "Redis TaskManager Example"

	// Skill information
	skillID          = "text_to_lower"
	skillName        = "Text to Lowercase"
	skillDescription = "Convert any text to lowercase"
)

var (
	// Skill configuration
	skillTags        = []string{"text", "conversion", "lowercase"}
	skillExamples    = []string{"Hello World!", "THIS IS UPPERCASE", "MiXeD cAsE tExT"}
	inputOutputModes = []string{"text"}
)

// ToLowerProcessor implements a simple text processing service that converts text to lowercase
type ToLowerProcessor struct{}

// ProcessMessage implements the taskmanager.MessageProcessor interface. One
// code path serves message/send and message/stream alike: the staged task
// updates stream live to message/stream subscribers, while message/send
// blocks and returns the final task snapshot.
func (p *ToLowerProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	log.Printf("Processing message: %s", ec.Message.MessageID)

	handle := taskmanager.NewTaskHandle(ctx, ec)

	// Extract text from message parts
	inputText := extractTextFromMessage(ec.Message)
	if inputText == "" {
		// A pure message reply: no task comes into existence this round.
		defer handle.Close()
		handle.Reply(protocol.NewAgentText("Error: No text found in message"))
		return handle.Events(), nil
	}

	// The staged conversion takes a couple of seconds: emit from a goroutine
	// so each update streams live instead of buffering until the round ends.
	go func() {
		defer handle.Close()
		p.processText(ctx, inputText, handle)
	}()

	return handle.Events(), nil
}

// sleepUnlessCanceled waits d, returning false if the round was canceled first.
func sleepUnlessCanceled(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// extractTextFromMessage extracts text content from message parts
func extractTextFromMessage(message protocol.Message) string {
	var inputText string
	for _, part := range message.Parts {
		if t := part.TextContent(); t != "" {
			inputText += t
		}
	}
	return inputText
}

// processText drives one working -> artifact -> completed task round with
// staged progress updates. Returning early on a canceled ctx closes the round
// without a terminal state, letting the framework persist CANCELED.
func (p *ToLowerProcessor) processText(ctx context.Context, inputText string, handle *taskmanager.TaskHandle) {
	// Step 1: Starting processing
	err := handle.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText(msgStarting))
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate analysis phase
	if !sleepUnlessCanceled(ctx, analysisDelay) {
		return
	}

	// Step 2: Analysis phase
	err = handle.UpdateTaskState(protocol.TaskStateWorking,
		protocol.NewAgentText(fmt.Sprintf(msgAnalyzing, len(inputText))))
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate processing phase
	if !sleepUnlessCanceled(ctx, processingDelay) {
		return
	}

	// Step 3: Processing phase
	err = handle.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText(msgProcessing))
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate actual processing
	if !sleepUnlessCanceled(ctx, conversionDelay) {
		return
	}

	// Process the text
	result := strings.ToLower(inputText)

	// Step 4: Creating artifact
	err = handle.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText(msgArtifact))
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Create an artifact with the processed text
	artifact := protocol.Artifact{
		ArtifactID:  protocol.GenerateArtifactID(),
		Name:        stringPtr(skillName),
		Description: stringPtr(skillDescription),
		Parts: []*protocol.Part{
			protocol.NewTextPart(result),
		},
		Metadata: map[string]interface{}{
			"operation":      skillID,
			"originalText":   inputText,
			"originalLength": len(inputText),
			"resultLength":   len(result),
			"processedAt":    time.Now().UTC().Format(time.RFC3339),
			"processingTime": "1.7s",
		},
	}

	// Add artifact to task
	if err := handle.AddArtifact(artifact, false, true); err != nil {
		log.Printf("Failed to add artifact: %v", err)
	}

	// Small delay to show artifact creation
	time.Sleep(artifactCreationDelay)

	// Update task to completed state with the result message. Ending the
	// round terminal replaces the former CleanTask: the framework owns the
	// task lifecycle (set redis.WithExpireTime to bound retention).
	err = handle.UpdateTaskState(protocol.TaskStateCompleted,
		protocol.NewAgentText(fmt.Sprintf(msgCompleted, inputText, result)))
	if err != nil {
		log.Printf("Failed to complete task: %v", err)
	}
}

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func main() {
	// Parse command line flags
	var redisAddr = flag.String("redis_addr", defaultRedisAddress, "Redis server address")
	var serverAddr = flag.String("addr", defaultServerAddress, "Server listen address (e.g., :8080 or localhost:8080)")
	var help = flag.Bool("help", false, "Show help message")
	var version = flag.Bool("version", false, "Show version information")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Text Case Converter Server - Redis TaskManager Example\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s                                # Use default settings\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --redis_addr localhost:6380    # Custom Redis port\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --addr :9000                   # Custom server port\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --redis_addr redis.example.com:6379 --addr :8080\n", os.Args[0])
	}

	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	if *version {
		fmt.Println("Text Case Converter Server v1.0.0")
		fmt.Println("Redis TaskManager Example")
		os.Exit(0)
	}

	// Create Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     *redisAddr,
		Password: "", // no password
		DB:       0,  // default DB
	})

	// Test Redis connection
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis at %s: %v", *redisAddr, err)
	}
	log.Printf("Connected to Redis at %s successfully", *redisAddr)

	// Create the toLower processor
	processor := &ToLowerProcessor{}

	// Create Redis TaskManager
	taskManager, err := redisTaskManager.NewTaskManager(processor, rdb)
	if err != nil {
		log.Fatalf("Failed to create Redis TaskManager: %v", err)
	}
	defer taskManager.Close()

	// Create agent card
	agentCard := server.AgentCard{
		Name:        serverName,
		Description: serverDescription,
		URL:         fmt.Sprintf("http://localhost%s/", *serverAddr),
		Version:     serverVersion,
		Provider: &server.AgentProvider{
			Organization: organizationName,
		},
		Capabilities: server.AgentCapabilities{
			Streaming:         boolPtr(true),
			PushNotifications: boolPtr(false),
		},
		DefaultInputModes:  inputOutputModes,
		DefaultOutputModes: inputOutputModes,
		Skills: []server.AgentSkill{
			{
				ID:          skillID,
				Name:        skillName,
				Description: stringPtr(skillDescription),
				Tags:        skillTags,
				Examples:    skillExamples,
				InputModes:  inputOutputModes,
				OutputModes: inputOutputModes,
			},
		},
	}

	// Create HTTP server
	agentServer, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}
	log.Printf("Starting Text Case Converter server on %s", *serverAddr)
	log.Printf("Redis backend: %s", *redisAddr)
	log.Printf("Try sending text like: 'Hello World!' and it will be converted to 'hello world!'")
	err = agentServer.Start(*serverAddr)
	if err != nil {
		log.Fatalf("Failed to start A2A server: %v", err)
	}
}
