// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
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
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
	redisTaskManager "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
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

// ProcessMessage processes incoming messages by converting text to lowercase.
// It supports both streaming and non-streaming modes of operation.
func (p *ToLowerProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	log.Printf("Processing message: %s", message.MessageID)

	// Extract text from message parts
	inputText := extractTextFromMessage(message)
	if inputText == "" {
		return &taskmanager.MessageProcessingResult{
			Result: &protocol.Message{
				Role: protocol.MessageRoleAgent,
				Parts: []protocol.Part{
					protocol.NewTextPart("Error: No text found in message"),
				},
			},
		}, nil
	}

	if options.Streaming {
		return p.processStreamingMode(inputText, message.ContextID, handle)
	}

	return p.processNonStreamingMode(inputText), nil
}

// extractTextFromMessage extracts text content from message parts
func extractTextFromMessage(message protocol.Message) string {
	var inputText string
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			inputText += textPart.Text
		}
	}
	return inputText
}

// processStreamingMode handles streaming processing with task updates
func (p *ToLowerProcessor) processStreamingMode(
	inputText string,
	contextID *string,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Build task for streaming mode
	taskID, err := handle.BuildTask(nil, contextID)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}

	// Subscribe to the task
	subscriber, err := handle.SubScribeTask(&taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to task: %w", err)
	}

	// Process asynchronously
	go p.processTextAsync(inputText, taskID, handle)

	return &taskmanager.MessageProcessingResult{
		StreamingEvents: subscriber,
	}, nil
}

// processNonStreamingMode handles direct processing without streaming
func (p *ToLowerProcessor) processNonStreamingMode(inputText string) *taskmanager.MessageProcessingResult {
	result := strings.ToLower(inputText)

	response := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(result),
		},
	}

	return &taskmanager.MessageProcessingResult{
		Result: response,
	}
}

func (p *ToLowerProcessor) processTextAsync(
	inputText string,
	taskID string,
	handle taskmanager.TaskHandler,
) {
	defer func() {
		err := handle.CleanTask(&taskID)
		if err != nil {
			log.Printf("Failed to clean task: %v", err)
		}
	}()

	// Step 1: Starting processing
	err := handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(msgStarting),
		},
	})
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate analysis phase
	time.Sleep(analysisDelay)

	// Step 2: Analysis phase
	err = handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf(msgAnalyzing, len(inputText))),
		},
	})
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate processing phase
	time.Sleep(processingDelay)

	// Step 3: Processing phase
	err = handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(msgProcessing),
		},
	})
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate actual processing
	time.Sleep(conversionDelay)

	// Process the text
	result := strings.ToLower(inputText)

	// Step 4: Creating artifact
	err = handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(msgArtifact),
		},
	})
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Create an artifact with the processed text
	artifact := protocol.Artifact{
		ArtifactID:  protocol.GenerateArtifactID(),
		Name:        stringPtr(skillName),
		Description: stringPtr(skillDescription),
		Parts: []protocol.Part{
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
	if err := handle.AddArtifact(&taskID, artifact, true, false); err != nil {
		log.Printf("Failed to add artifact: %v", err)
	}

	// Small delay to show artifact creation
	time.Sleep(artifactCreationDelay)

	// Send final status with result message
	finalMessage := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf(msgCompleted, inputText, result)),
		},
	}

	// Update task to completed state
	err = handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, finalMessage)
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
	taskManager, err := redisTaskManager.NewTaskManager(rdb, processor)
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
	agentServer, err := server.NewA2AServer(agentCard, taskManager)
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
