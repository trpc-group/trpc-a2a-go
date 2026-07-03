// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a basic A2A agent example.
//
// It is the minimal-edit port of the former v0.x TaskHandler-style processor:
// the body stays synchronous and keeps the old verbs via taskmanager.TaskHandle.
// One code path serves message/send and message/stream alike — the framework
// derives the unary result or the live stream from the emitted events.
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

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
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
	// Multi-turn session state, keyed by conversation contextID.
	multiTurnSessions map[string]multiTurnSession
}

// ProcessMessage implements the taskmanager.MessageProcessor interface. The
// body is fully synchronous: emits made before Events() never block, so the
// old v0.x writing style works as-is.
func (p *basicMessageProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	log.Infof("Processing basic message with ID: %s", ec.Message.MessageID)

	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	// Extract text from the incoming message
	text := extractText(ec.Message)
	if text == "" {
		// A pure message reply: no task comes into existence this round.
		handle.Reply(taskmanager.ReplyText("input message must contain text"))
		return handle.Events(), nil
	}

	// The conversation context ID drives session management (the framework
	// generates one when the request carries none).
	contextID := handle.GetContextID()

	// Continue an in-flight multi-turn session for this conversation.
	if session, exists := p.multiTurnSessions[contextID]; exists && !session.complete {
		p.continueMultiTurnSession(handle, text, contextID, session)
		return handle.Events(), nil
	}

	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(parts[0])
	var content string
	if len(parts) > 1 {
		content = parts[1]
	}

	switch command {
	case modeMultiStep:
		// Suspend the task awaiting the processing mode: the follow-up
		// message arrives as a new round with ExecContext.Task set.
		p.multiTurnSessions[contextID] = multiTurnSession{stage: 1}
		handle.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText(
			"This is a multi-step interaction. Please select a processing mode:\n"+
				"- reverse: Reverses the text\n"+
				"- uppercase: Converts text to uppercase\n"+
				"- lowercase: Converts text to lowercase\n"+
				"- count: Counts words and characters"))
	case modeInputExample:
		p.multiTurnSessions[contextID] = multiTurnSession{stage: 2, mode: modeReverse}
		handle.UpdateTaskState(protocol.TaskStateInputRequired,
			taskmanager.ReplyText("Please provide more information to continue:"))
	case modeHelp:
		// Plain answers need no task either.
		handle.Reply(taskmanager.ReplyText(p.processTextWithMode(content, command)))
	default:
		p.processCommand(handle, contextID, command, content)
	}
	return handle.Events(), nil
}

// processCommand runs one text command as a working -> artifact -> completed
// task round.
func (p *basicMessageProcessor) processCommand(
	handle *taskmanager.TaskHandle,
	contextID string,
	command string,
	content string,
) {
	handle.UpdateTaskState(protocol.TaskStateWorking, nil)

	// Simulate processing delay for demonstration
	time.Sleep(500 * time.Millisecond)

	result := p.processTextWithMode(content, command)
	handle.AddArtifact(protocol.Artifact{
		ArtifactID:  "processed-text-" + handle.TaskID(),
		Name:        stringPtr("Processed Text"),
		Description: stringPtr(fmt.Sprintf("Text processed with mode: %s", command)),
		Parts:       []*protocol.Part{protocol.NewTextPart(result)},
		Metadata: map[string]interface{}{
			"operation":    command,
			"originalText": content,
			"processedAt":  time.Now().UTC().Format(time.RFC3339),
			"contextID":    contextID,
		},
	}, true)
	handle.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText(result))
}

// continueMultiTurnSession processes the next step of a multi-turn interaction.
func (p *basicMessageProcessor) continueMultiTurnSession(
	handle *taskmanager.TaskHandle,
	text string,
	contextID string,
	session multiTurnSession,
) {
	switch session.stage {
	case 1:
		// First response received - this is the mode
		session.mode = strings.ToLower(strings.TrimSpace(text))
		session.stage = 2
		p.multiTurnSessions[contextID] = session

		// Ask for the text to process
		handle.UpdateTaskState(protocol.TaskStateInputRequired,
			taskmanager.ReplyText("Please enter the text you want to process:"))
	case 2:
		// Second response received - this is the text to process
		session.text = text
		session.stage = 3
		session.complete = true
		p.multiTurnSessions[contextID] = session

		// Process the text based on the selected mode
		result := p.processTextWithMode(session.text, session.mode)
		handle.AddArtifact(protocol.Artifact{
			ArtifactID:  "processed-text-" + handle.TaskID(),
			Name:        stringPtr("Processed Text"),
			Description: stringPtr(fmt.Sprintf("Text processed with mode: %s", session.mode)),
			Parts:       []*protocol.Part{protocol.NewTextPart(result)},
			Metadata: map[string]interface{}{
				"operation":    session.mode,
				"originalText": session.text,
				"processedAt":  time.Now().UTC().Format(time.RFC3339),
				"sessionStage": session.stage,
				"contextID":    contextID,
			},
		}, true)
		handle.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText(result))
	}
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
	var texts []string
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
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
		multiTurnSessions: make(map[string]multiTurnSession),
	}

	// Create the base TaskManager with built-in push notification storage support
	baseTaskManager, err := memory.NewTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Wrap the MemoryTaskManager with our sender that adds webhook functionality
	taskManager := newPushNotificationSender(baseTaskManager)

	// Create the A2A server instance using the factory from server package
	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard), server.WithCORSEnabled(!noCORS))
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
) (*protocol.SendMessageResponse, error) {
	result, err := p.TaskManager.OnSendMessage(ctx, params)
	if err == nil && result != nil {
		if task := result.GetTask(); task != nil {
			go p.maybeSendStatusPushNotification(ctx, task.ID, task.Status.State)
		}
	}
	return result, err
}

// OnSendMessageStream overrides to add webhook notification
func (p *pushNotificationSender) OnSendMessageStream(
	ctx context.Context,
	params protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	eventChan, err := p.TaskManager.OnSendMessageStream(ctx, params)
	if err != nil {
		return nil, err
	}

	wrappedChan := make(chan protocol.StreamResponse)

	go func() {
		defer close(wrappedChan)
		for event := range eventChan {
			wrappedChan <- event

			if statusEvent := event.GetStatusUpdate(); statusEvent != nil {
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

	// Send the notification (flat v1.0 config -> delivery-details view)
	p.sendPushNotification(*config.Details(), payload)
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
