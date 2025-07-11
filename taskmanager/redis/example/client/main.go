// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main provides a Redis TaskManager example client that demonstrates
// how to interact with the Redis-based task manager server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

const (
	// Default configuration values
	defaultServerURL = "http://localhost:8080/"
	defaultInputText = "Hello World! THIS IS A TEST MESSAGE."

	// Client information
	clientName    = "Text Case Converter Client"
	clientVersion = "1.0.0"

	// Timing constants
	clientTimeout    = 30 * time.Second
	progressInterval = 500 * time.Millisecond

	// Output prefixes
	prefixSuccess    = "[SUCCESS]"
	prefixResult     = "[RESULT]"
	prefixWarning    = "[WARNING]"
	prefixStreaming  = "[STREAMING]"
	prefixProcessing = "[PROCESSING]"
	prefixCompleted  = "[COMPLETED]"
	prefixTask       = "[TASK]"
	prefixMessage    = "[MESSAGE]"
	prefixFinished   = "[FINISHED]"
	prefixArtifact   = "[ARTIFACT]"
	prefixName       = "[NAME]"
	prefixDesc       = "[DESC]"
	prefixContent    = "[CONTENT]"
	prefixMetadata   = "[METADATA]"
	prefixUnknown    = "[UNKNOWN]"
	prefixTimeout    = "[TIMEOUT]"
)

var (
	// Progress indicator characters
	progressChars = []string{".", "..", "...", "...."}
)

func main() {
	// Parse command line arguments
	var serverURL = flag.String("addr", defaultServerURL, "Server URL")
	var inputText = flag.String("text", defaultInputText, "Input text to process")
	var streamingOnly = flag.Bool("streaming", false, "Only test streaming mode")
	var nonStreamingOnly = flag.Bool("non-streaming", false, "Only test non-streaming mode")
	var verbose = flag.Bool("verbose", false, "Enable verbose output")
	var help = flag.Bool("help", false, "Show help message")
	var version = flag.Bool("version", false, "Show version information")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s - Redis TaskManager Example\n\n", clientName)
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s                                        # Use default settings\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --text \"Custom Text\"                   # Custom input text\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --addr http://localhost:9000/          # Custom server URL\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --streaming                            # Only test streaming mode\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --non-streaming                        # Only test non-streaming mode\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --verbose                              # Enable verbose output\n", os.Args[0])
	}

	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	if *version {
		fmt.Printf("%s %s\n", clientName, clientVersion)
		fmt.Println("Redis TaskManager Example")
		os.Exit(0)
	}

	// Validate flags
	if *streamingOnly && *nonStreamingOnly {
		log.Fatal("Cannot specify both --streaming and --non-streaming flags")
	}

	// Create A2A client
	a2aClient, err := client.NewA2AClient(
		*serverURL,
		client.WithTimeout(clientTimeout),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	ctx := context.Background()

	// Display what we're going to do
	fmt.Printf("=== %s ===\n", clientName)
	fmt.Printf("Server: %s\n", *serverURL)
	fmt.Printf("Input text: '%s'\n", *inputText)
	if *verbose {
		fmt.Printf("Verbose mode: enabled\n")
	}
	fmt.Println()

	// Test based on flags
	if !*streamingOnly {
		fmt.Println("Test 1: Non-streaming conversion")
		runNonStreamingDemo(ctx, a2aClient, *inputText, *verbose)

		if !*nonStreamingOnly {
			fmt.Println("\n" + strings.Repeat("=", 50) + "\n")
		}
	}

	if !*nonStreamingOnly {
		fmt.Println("Test 2: Streaming conversion with task updates")
		runStreamingDemo(ctx, a2aClient, *inputText, *verbose)
	}
}

func runNonStreamingDemo(ctx context.Context, client *client.A2AClient, inputText string, verbose bool) {
	message := protocol.Message{
		Role: protocol.MessageRoleUser,
		Parts: []protocol.Part{
			protocol.NewTextPart(inputText),
		},
	}

	params := protocol.SendMessageParams{
		Message: message,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(true), // Non-streaming
		},
	}

	if verbose {
		fmt.Printf("-> Sending non-streaming request...\n")
	}

	start := time.Now()
	result, err := client.SendMessage(ctx, params)
	duration := time.Since(start)

	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("%s Processing time: %v\n", prefixSuccess, duration)

	if response, ok := result.Result.(*protocol.Message); ok {
		for i, part := range response.Parts {
			if textPart, ok := part.(*protocol.TextPart); ok {
				fmt.Printf("%s %d: '%s'\n", prefixResult, i+1, textPart.Text)
			}
		}
	} else {
		fmt.Printf("%s Unexpected result type: %T\n", prefixWarning, result.Result)
	}
}

func runStreamingDemo(ctx context.Context, client *client.A2AClient, inputText string, verbose bool) {
	eventChan, err := startStreamingRequest(ctx, client, inputText, verbose)
	if err != nil {
		log.Printf("Failed to start streaming: %v", err)
		return
	}

	streamProcessor := &streamEventProcessor{
		verbose:        verbose,
		startTime:      time.Now(),
		timeout:        time.After(clientTimeout),
		progressTicker: time.NewTicker(progressInterval),
	}
	defer streamProcessor.progressTicker.Stop()

	streamProcessor.processEvents(eventChan)
}

// startStreamingRequest initiates a streaming request and returns the event channel
func startStreamingRequest(ctx context.Context, client *client.A2AClient, inputText string, verbose bool) (<-chan protocol.StreamingMessageEvent, error) {
	message := protocol.Message{
		Role: protocol.MessageRoleUser,
		Parts: []protocol.Part{
			protocol.NewTextPart(inputText),
		},
	}

	params := protocol.SendMessageParams{
		Message: message,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(false), // Streaming
		},
	}

	if verbose {
		fmt.Printf("-> Starting streaming request...\n")
	}

	return client.StreamMessage(ctx, params)
}

// streamEventProcessor handles processing of streaming events
type streamEventProcessor struct {
	verbose        bool
	startTime      time.Time
	timeout        <-chan time.Time
	progressTicker *time.Ticker
	taskID         string
	eventCount     int
	progressIndex  int
}

// processEvents processes streaming events from the event channel
func (p *streamEventProcessor) processEvents(eventChan <-chan protocol.StreamingMessageEvent) {
	fmt.Printf("%s Processing events:\n", prefixStreaming)

	for {
		select {
		case <-p.progressTicker.C:
			p.handleProgressTick()

		case event, ok := <-eventChan:
			if !ok {
				p.handleStreamComplete()
				return
			}
			p.handleStreamEvent(event)

		case <-p.timeout:
			fmt.Printf("%s Waiting for events\n", prefixTimeout)
			return
		}
	}
}

// handleProgressTick updates the progress indicator
func (p *streamEventProcessor) handleProgressTick() {
	if p.eventCount > 0 && p.verbose {
		p.progressIndex = (p.progressIndex + 1) % len(progressChars)
		fmt.Printf("\r%s %s", prefixProcessing, progressChars[p.progressIndex])
	}
}

// handleStreamComplete handles stream completion
func (p *streamEventProcessor) handleStreamComplete() {
	duration := time.Since(p.startTime)
	if p.verbose {
		fmt.Printf("\r")
	}
	fmt.Printf("%s Stream finished (%d events, %v total)\n", prefixCompleted, p.eventCount, duration)
}

// handleStreamEvent processes a single stream event
func (p *streamEventProcessor) handleStreamEvent(event protocol.StreamingMessageEvent) {
	p.eventCount++

	// Clear progress indicator
	if p.verbose && p.eventCount > 1 {
		fmt.Printf("\r")
	}

	switch result := event.Result.(type) {
	case *protocol.TaskStatusUpdateEvent:
		p.handleTaskStatusEvent(result)
	case *protocol.TaskArtifactUpdateEvent:
		p.handleTaskArtifactEvent(result)
	default:
		fmt.Printf("%s Event type: %T\n", prefixUnknown, result)
	}
}

// handleTaskStatusEvent processes task status update events
func (p *streamEventProcessor) handleTaskStatusEvent(event *protocol.TaskStatusUpdateEvent) {
	if p.taskID == "" {
		p.taskID = event.TaskID
		if p.verbose {
			fmt.Printf("%s ID: %s\n", prefixTask, event.TaskID)
		}
	}

	statusPrefix := getStatusPrefix(event.Status.State)
	fmt.Printf("[%s] Task State: %s", statusPrefix, event.Status.State)

	if p.verbose {
		fmt.Printf(" (Event #%d)", p.eventCount)
	}
	fmt.Println()

	p.displayTaskMessage(event.Status.Message)

	if event.IsFinal() {
		duration := time.Since(p.startTime)
		fmt.Printf("%s Task completed! (Total time: %v)\n", prefixFinished, duration)
	}
}

// handleTaskArtifactEvent processes task artifact update events
func (p *streamEventProcessor) handleTaskArtifactEvent(event *protocol.TaskArtifactUpdateEvent) {
	fmt.Printf("%s ID: %s\n", prefixArtifact, event.Artifact.ArtifactID)

	if event.Artifact.Name != nil {
		fmt.Printf("   %s %s\n", prefixName, *event.Artifact.Name)
	}

	if event.Artifact.Description != nil {
		fmt.Printf("   %s %s\n", prefixDesc, *event.Artifact.Description)
	}

	p.displayArtifactContent(event.Artifact.Parts)

	if p.verbose {
		p.displayArtifactMetadata(event.Artifact.Metadata)
	}
}

// displayTaskMessage displays task status message if present
func (p *streamEventProcessor) displayTaskMessage(message *protocol.Message) {
	if message != nil {
		for _, part := range message.Parts {
			if textPart, ok := part.(*protocol.TextPart); ok {
				fmt.Printf("   %s %s\n", prefixMessage, textPart.Text)
			}
		}
	}
}

// displayArtifactContent displays artifact content parts
func (p *streamEventProcessor) displayArtifactContent(parts []protocol.Part) {
	for _, part := range parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			fmt.Printf("   %s '%s'\n", prefixContent, textPart.Text)
		}
	}
}

// displayArtifactMetadata displays artifact metadata if available
func (p *streamEventProcessor) displayArtifactMetadata(metadata map[string]interface{}) {
	if len(metadata) > 0 {
		fmt.Printf("   %s\n", prefixMetadata)
		for key, value := range metadata {
			fmt.Printf("      %s: %v\n", key, value)
		}
	}
}

// getStatusPrefix returns an appropriate prefix for the task status
func getStatusPrefix(state protocol.TaskState) string {
	switch state {
	case protocol.TaskStateWorking:
		return "WORKING"
	case protocol.TaskStateCompleted:
		return "SUCCESS"
	case protocol.TaskStateFailed:
		return "FAILED"
	case protocol.TaskStateCanceled:
		return "CANCELLED"
	default:
		return "STATUS"
	}
}

func boolPtr(b bool) *bool {
	return &b
}
