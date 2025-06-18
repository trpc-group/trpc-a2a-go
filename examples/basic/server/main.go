// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a basic A2A agent example.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Command modes for the text processor
const (
	modeReverse      = "reverse"
	modeUppercase    = "uppercase"
	modeLowercase    = "lowercase"
	modeCount        = "count"
	modeHelp         = "help"
	modeMultiStep    = "multi"
	modeInputExample = "example"
)

// multiTurnSession tracks state for a multi-turn interaction
type multiTurnSession struct {
	stage    int
	text     string
	mode     string
	complete bool
}

// basicMessageProcessor implements the taskmanager.MessageProcessor interface
type basicMessageProcessor struct {
	// Flag to determine if we should use streaming mode
	useStreaming bool

	// Added for multi-turn session handling - now keyed by contextID instead of taskID
	multiTurnSessions map[string]multiTurnSession
}

// ProcessMessage implements the taskmanager.MessageProcessor interface
func (p *basicMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	log.Infof("Processing basic message with ID: %s", message.MessageID)

	// Initialize multi-turn sessions map if not already initialized
	if p.multiTurnSessions == nil {
		p.multiTurnSessions = make(map[string]multiTurnSession)
	}

	// Extract text from the incoming message
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text"
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

	// Get context ID for session management
	contextID := handle.GetContextID()
	if contextID == "" {
		// No context ID available, treat as simple command processing
		return p.processSimpleCommand(text)
	}

	if options.Streaming {
		// Streaming mode - use task-based processing with full features
		taskID, err := handle.BuildTask(nil, &contextID)
		if err != nil {
			return nil, fmt.Errorf("failed to build task: %w", err)
		}

		log.Infof("Created streaming task %s for processing", taskID)

		// Subscribe to the task for streaming events
		subscriber, err := handle.SubScribeTask(&taskID)
		if err != nil {
			return nil, fmt.Errorf("failed to subscribe to task: %w", err)
		}

		// Process asynchronously with full multi-turn support
		go p.processMessageAsync(ctx, text, contextID, taskID, handle)

		return &taskmanager.MessageProcessingResult{
			StreamingEvents: subscriber,
		}, nil
	}
	// Non-streaming mode - check for multi-turn interactions
	session, exists := p.multiTurnSessions[contextID]

	if exists && !session.complete {
		// Continue existing multi-turn session
		return p.processMultiTurnSession(ctx, text, contextID, handle, session, options)
	}

	// New interaction - check if it requires multi-turn handling
	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(parts[0])

	if command == modeMultiStep || command == modeInputExample {
		// Create task for multi-turn interaction even in non-streaming mode
		taskID, err := handle.BuildTask(nil, &contextID)
		if err != nil {
			return nil, fmt.Errorf("failed to build task: %w", err)
		}

		go p.processMessageAsync(ctx, text, contextID, taskID, handle)

		// Return a message indicating async processing
		responseMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart("Multi-turn interaction started. Please continue the conversation.")},
		)
		return &taskmanager.MessageProcessingResult{
			Result: &responseMessage,
		}, nil
	}

	// Simple command processing
	return p.processSimpleCommand(text)
}

// processSimpleCommand handles direct command processing without tasks
func (p *basicMessageProcessor) processSimpleCommand(text string) (*taskmanager.MessageProcessingResult, error) {
	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(parts[0])

	var content string
	if len(parts) > 1 {
		content = parts[1]
	}

	// Process the content based on command
	result := p.processTextWithMode(content, command)
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(result)},
	)

	return &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}, nil
}

// processMultiTurnSession handles continuing a multi-turn session in non-streaming mode
func (p *basicMessageProcessor) processMultiTurnSession(
	ctx context.Context,
	text string,
	contextID string,
	handle taskmanager.TaskHandler,
	session multiTurnSession,
	options taskmanager.ProcessOptions,
) (*taskmanager.MessageProcessingResult, error) {
	// Create task for multi-turn processing
	taskID, err := handle.BuildTask(nil, &contextID)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}

	go p.handleMultiTurnSessionAsync(ctx, taskID, text, contextID, handle, session)

	// Return message indicating processing
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart("Processing your multi-turn request...")},
	)
	return &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}, nil
}

// processTextWithMode processes text with the specified mode
func (p *basicMessageProcessor) processTextWithMode(text, mode string) string {
	switch mode {
	case modeReverse:
		// Simple text reversal
		runes := []rune(text)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return fmt.Sprintf("Reversed: %s", string(runes))
	case modeUppercase:
		return strings.ToUpper(text)
	case modeLowercase:
		return strings.ToLower(text)
	case modeCount:
		words := len(strings.Fields(text))
		chars := len(text)
		return fmt.Sprintf("Word count: %d, Character count: %d", words, chars)
	case modeHelp:
		return `Available commands:
- reverse <text>: Reverse the given text
- uppercase <text>: Convert text to uppercase
- lowercase <text>: Convert text to lowercase
- count <text>: Count words and characters
- multi-step: Start a multi-turn interaction
- input-example: Example of input-required state
- help: Show this help message

Example: reverse hello world`
	default:
		return fmt.Sprintf("Unknown mode '%s'. Use 'help' for available commands.", mode)
	}
}

// extractText extracts text content from a message
func extractText(message protocol.Message) string {
	var parts []string
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			parts = append(parts, textPart.Text)
		}
	}
	return strings.Join(parts, " ")
}

// main is the entry point for the server
func main() {
	// Command-line flags for server configuration
	var (
		host          string
		port          int
		description   string
		noCORS        bool
		forceNoStream bool // Flag to disable streaming
	)

	flag.StringVar(&host, "host", "localhost", "Server host address")
	flag.IntVar(&port, "port", 8080, "Server port")
	flag.StringVar(&description, "desc", "A versatile A2A example agent that processes text", "Agent description")
	flag.BoolVar(&noCORS, "no-cors", false, "Disable CORS headers")
	flag.BoolVar(&forceNoStream, "no-stream", false, "Disable streaming capability")
	flag.Parse()

	address := fmt.Sprintf("%s:%d", host, port)
	// Assuming HTTP for simplicity, HTTPS is recommended for production
	serverURL := fmt.Sprintf("http://%s/", address)

	// Description based on streaming capability
	description += " with streaming support"
	if !forceNoStream {
		description += " and push notifications"
	}

	// Create the agent card using types from the server package
	agentCard := server.AgentCard{
		Name:        "Text Processing Agent",
		Description: description,
		URL:         serverURL,
		Version:     "2.0.0", // Updated version
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(!forceNoStream), // Support streaming based on flag
			PushNotifications:      boolPtr(true),           // Enable push notifications
			StateTransitionHistory: boolPtr(true),           // MemoryTaskManager stores history
		},
		// Support text input/output
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_processor_reverse",
				Name:        "Text Reverser",
				Description: stringPtr("Input: reverse hello\nOutput: Reversed: olleh"),
				Tags:        []string{"text", "reverse"},
				Examples:    []string{"reverse hello world", "reverse The quick brown fox"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_uppercase",
				Name:        "Uppercase Converter",
				Description: stringPtr("Input: uppercase hello world\nOutput: HELLO WORLD"),
				Tags:        []string{"text", "uppercase"},
				Examples:    []string{"uppercase hello world", "uppercase Example text"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_lowercase",
				Name:        "Lowercase Converter",
				Description: stringPtr("Input: lowercase HELLO\nOutput: hello"),
				Tags:        []string{"text", "lowercase"},
				Examples:    []string{"lowercase HELLO WORLD", "lowercase TEXT"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_count",
				Name:        "Word Counter",
				Description: stringPtr("Input: count hello world\nOutput: Word count: 2, Character count: 11"),
				Tags:        []string{"text", "count"},
				Examples:    []string{"count The quick brown fox", "count hello world"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_multistep",
				Name:        "Multi-Step Processor",
				Description: stringPtr("Input: multi-step\nStarts an interactive multi-turn conversation"),
				Tags:        []string{"interactive", "multi-turn"},
				Examples:    []string{"multi-step"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_help",
				Name:        "Help Guide",
				Description: stringPtr("Input: help\nOutput: List of available commands and usage"),
				Tags:        []string{"help"},
				Examples:    []string{"help"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// Create the MessageProcessor (agent logic)
	processor := &basicMessageProcessor{
		useStreaming:      !forceNoStream,
		multiTurnSessions: make(map[string]multiTurnSession),
	}

	// Create the base TaskManager with built-in push notification storage support
	baseTaskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Wrap the MemoryTaskManager with our sender that adds webhook functionality
	taskManager := newPushNotificationSender(baseTaskManager)

	// Create the A2A server instance using the factory from server package
	srv, err := server.NewA2AServer(agentCard, taskManager, server.WithCORSEnabled(!noCORS))
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start the server in a separate goroutine
	go func() {
		// Use log.Printf for informational message, not Fatal
		log.Infof("Text Processing Agent server starting on %s (CORS enabled: %t, Streaming: %t, Push: %t)",
			address, !noCORS, !forceNoStream, true)
		if err := srv.Start(address); err != nil {
			// Fatalf will exit the program if the server fails to start
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Wait for an interrupt or termination signal
	<-sigChan
	log.Info("Shutdown signal received, initiating graceful shutdown...")

	// Create a context with a timeout for graceful shutdown
	// Allow 10 seconds for existing requests to finish
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Attempt to stop the server gracefully
	if err := srv.Stop(ctx); err != nil {
		log.Errorf("Server shutdown failed: %v", err)
	} else {
		log.Info("Server exited gracefully.")
	}
}

// Helper function to create a string pointer
func stringPtr(s string) *string {
	return &s
}

// Helper function to create a boolean pointer
func boolPtr(b bool) *bool {
	return &b
}

// pushNotificationSender wraps a TaskManager to add webhook notification sending
// while using the built-in storage for push notification configurations
type pushNotificationSender struct {
	// Embed the underlying task manager to inherit methods
	taskmanager.TaskManager
}

// newPushNotificationSender creates a new PushNotificationSender wrapping a given TaskManager
func newPushNotificationSender(base taskmanager.TaskManager) *pushNotificationSender {
	return &pushNotificationSender{
		TaskManager: base,
	}
}

// OnSendMessage overrides to add webhook notification
func (p *pushNotificationSender) OnSendMessage(
	ctx context.Context,
	params protocol.SendMessageParams,
) (*protocol.MessageResult, error) {
	// Call the underlying implementation
	result, err := p.TaskManager.OnSendMessage(ctx, params)
	if err == nil && result != nil {
		// Check if result is a task and send notification if needed
		if task, ok := result.Result.(*protocol.Task); ok {
			go p.maybeSendStatusPushNotification(ctx, task.ID, task.Status.State)
		}
	}
	return result, err
}

// OnSendMessageStream overrides to add webhook notification
func (p *pushNotificationSender) OnSendMessageStream(
	ctx context.Context,
	params protocol.SendMessageParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	// Call the underlying implementation
	eventChan, err := p.TaskManager.OnSendMessageStream(ctx, params)
	if err != nil {
		return nil, err
	}

	// Create a wrapper channel to monitor events and send notifications
	wrappedChan := make(chan protocol.StreamingMessageEvent)

	go func() {
		defer close(wrappedChan)
		for event := range eventChan {
			// Forward the event
			wrappedChan <- event

			// Check if it's a task status update and send notification
			if statusEvent, ok := event.Result.(*protocol.TaskStatusUpdateEvent); ok {
				go p.maybeSendStatusPushNotification(ctx, statusEvent.TaskID, statusEvent.Status.State)
			}
		}
	}()

	return wrappedChan, nil
}

// OnCancelTask overrides to add webhook notification (keep existing deprecated method support)
func (p *pushNotificationSender) OnCancelTask(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	// Call the underlying implementation
	task, err := p.TaskManager.OnCancelTask(ctx, params)
	if err == nil && task != nil {
		// Send push notification if task was canceled successfully
		go p.maybeSendStatusPushNotification(ctx, task.ID, task.Status.State)
	}
	return task, err
}

// maybeSendStatusPushNotification sends a status notification if configured for the task
func (p *pushNotificationSender) maybeSendStatusPushNotification(
	ctx context.Context,
	taskID string,
	status protocol.TaskState,
) {
	// Get the push notification configuration for this task
	config, err := p.TaskManager.OnPushNotificationGet(
		ctx, protocol.TaskIDParams{ID: taskID},
	)
	if err != nil {
		// No configuration found or error occurred - no notification to send
		return
	}

	// Create notification payload
	payload := map[string]interface{}{
		"id":     taskID,
		"status": status,
	}

	// Send the notification
	p.sendPushNotification(config.PushNotificationConfig, payload)
}

// sendPushNotification sends a notification to the configured webhook URL
func (p *pushNotificationSender) sendPushNotification(
	config protocol.PushNotificationConfig,
	payload interface{},
) {
	// Create JSON payload
	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Errorf("Error marshaling push notification: %v", err)
		return
	}

	// Create HTTP request
	req, err := http.NewRequest(http.MethodPost, config.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Errorf("Error creating push notification request: %v", err)
		return
	}

	// Set content type
	req.Header.Set("Content-Type", "application/json")

	// Add authentication if configured
	if config.Token != "" {
		// Simple token-based auth using Bearer scheme
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.Token))
	}

	// Send the request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("Error sending push notification: %v", err)
		return
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Errorf("Push notification failed with status %d: %s", resp.StatusCode, string(body))
		return
	}

	log.Infof("Push notification sent successfully to %s", config.URL)
}

// processMessageAsync handles message processing asynchronously with full task management
func (p *basicMessageProcessor) processMessageAsync(
	ctx context.Context,
	text string,
	contextID string,
	taskID string,
	handle taskmanager.TaskHandler,
) {
	// Update task to working state
	err := handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, nil)
	if err != nil {
		log.Errorf("Failed to update task state: %v", err)
		return
	}

	// Check for continuation of a multi-turn session using contextID
	session, exists := p.multiTurnSessions[contextID]

	if exists && !session.complete {
		p.handleMultiTurnSessionAsync(ctx, taskID, text, contextID, handle, session)
		return
	}

	// New interaction - determine mode and process accordingly
	p.handleNewInteractionAsync(ctx, taskID, text, contextID, handle)
}

// handleMultiTurnSessionAsync processes the next step in a multi-turn interaction
func (p *basicMessageProcessor) handleMultiTurnSessionAsync(
	ctx context.Context,
	taskID string,
	text string,
	contextID string,
	handle taskmanager.TaskHandler,
	session multiTurnSession,
) {
	// Update session with new input
	switch session.stage {
	case 1:
		// First response received - this is the mode
		session.mode = strings.ToLower(strings.TrimSpace(text))
		session.stage = 2

		// Ask for the text to process
		msg := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart("Please enter the text you want to process:")},
		)

		// Update task to input-required state
		err := handle.UpdateTaskState(&taskID, protocol.TaskStateInputRequired, &msg)
		if err != nil {
			log.Errorf("Failed to update task status: %v", err)
			return
		}

		// Store updated session using contextID
		p.multiTurnSessions[contextID] = session

	case 2:
		// Second response received - this is the text to process
		session.text = text
		session.stage = 3
		session.complete = true

		// Process the text based on the selected mode
		result := p.processTextWithMode(session.text, session.mode)

		// Create the completed message
		finalMsg := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(result)},
		)

		// Add artifact with the processed result
		artifact := protocol.Artifact{
			ArtifactID:  "processed-text-" + taskID,
			Name:        stringPtr("Processed Text"),
			Description: stringPtr(fmt.Sprintf("Text processed with mode: %s", session.mode)),
			Parts:       []protocol.Part{protocol.NewTextPart(result)},
			Metadata: map[string]interface{}{
				"operation":    session.mode,
				"originalText": session.text,
				"processedAt":  time.Now().UTC().Format(time.RFC3339),
				"sessionStage": session.stage,
				"contextID":    contextID,
			},
		}

		// Add artifact to task
		if err := handle.AddArtifact(&taskID, artifact, true, false); err != nil {
			log.Errorf("Failed to add artifact: %v", err)
		}

		// Update task to completed state
		err := handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, &finalMsg)
		if err != nil {
			log.Errorf("Failed to complete task: %v", err)
		}

		// Update session in map
		p.multiTurnSessions[contextID] = session
	}
}

// handleNewInteractionAsync processes a new interaction
func (p *basicMessageProcessor) handleNewInteractionAsync(
	ctx context.Context,
	taskID string,
	text string,
	contextID string,
	handle taskmanager.TaskHandler,
) {
	// Check for cancellation via context
	if err := ctx.Err(); err != nil {
		log.Errorf("Task %s cancelled during processing: %v", taskID, err)
		_ = handle.UpdateTaskState(&taskID, protocol.TaskStateCanceled, nil)
		return
	}

	// Parse the first word as the command
	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(parts[0])

	// Handle multi-step mode
	if command == modeMultiStep {
		session := multiTurnSession{
			stage:    1,
			complete: false,
		}

		// Store the session using contextID
		p.multiTurnSessions[contextID] = session

		// Ask for the processing mode
		msg := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(
				"This is a multi-step interaction. Please select a processing mode:\n" +
					"- reverse: Reverses the text\n" +
					"- uppercase: Converts text to uppercase\n" +
					"- lowercase: Converts text to lowercase\n" +
					"- count: Counts words and characters")},
		)

		// Update task to input-required state
		err := handle.UpdateTaskState(&taskID, protocol.TaskStateInputRequired, &msg)
		if err != nil {
			log.Errorf("Failed to update task status: %v", err)
		}
		return
	}

	// Handle example input-required state
	if command == modeInputExample {
		msg := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart("Please provide more information to continue:")},
		)

		// Update task to input-required state
		err := handle.UpdateTaskState(&taskID, protocol.TaskStateInputRequired, &msg)
		if err != nil {
			log.Errorf("Failed to update task status: %v", err)
		}

		// Create a session for the follow-up
		session := multiTurnSession{
			stage:    2,           // Skip to stage 2 (text input)
			mode:     modeReverse, // Default to reverse mode
			complete: false,
		}
		p.multiTurnSessions[contextID] = session
		return
	}

	// For direct processing (non-multi-turn), extract the rest as content
	var content string
	if len(parts) > 1 {
		content = parts[1]
	} else {
		content = "" // No content provided, command only
	}

	// Simulate processing delay for demonstration
	time.Sleep(500 * time.Millisecond)

	// Process the content based on command
	result := p.processTextWithMode(content, command)

	// Create artifact with the processed result
	artifact := protocol.Artifact{
		ArtifactID:  "processed-text-" + taskID,
		Name:        stringPtr("Processed Text"),
		Description: stringPtr(fmt.Sprintf("Text processed with mode: %s", command)),
		Parts:       []protocol.Part{protocol.NewTextPart(result)},
		Metadata: map[string]interface{}{
			"operation":    command,
			"originalText": content,
			"processedAt":  time.Now().UTC().Format(time.RFC3339),
			"directMode":   true,
			"contextID":    contextID,
		},
	}

	// Add artifact to task
	if err := handle.AddArtifact(&taskID, artifact, true, false); err != nil {
		log.Errorf("Failed to add artifact: %v", err)
	}

	// Create final message
	finalMsg := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(result)},
	)

	// Update task to completed state
	err := handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, &finalMsg)
	if err != nil {
		log.Errorf("Failed to complete task: %v", err)
	}
}
