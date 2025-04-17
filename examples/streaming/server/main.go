// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a streaming server for the A2A protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// streamingTaskProcessor implements the TaskProcessor interface for streaming responses.
// This processor breaks the input text into chunks and sends them back as a stream.
type streamingTaskProcessor struct{}

// Process implements the core streaming logic.
// It breaks the input text into chunks and sends them back incrementally.
func (p *streamingTaskProcessor) Process(
	ctx context.Context,
	taskID string,
	message protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	log.Printf("Processing streaming task %s...", taskID)

	// Extract text from the incoming message.
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text"
		log.Printf("Task %s failed: %s", taskID, errMsg)

		// Update status to Failed via handle.
		failedMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		_ = handle.UpdateStatus(protocol.TaskStateFailed, &failedMessage)
		return fmt.Errorf(errMsg)
	}

	// Check if this is a streaming request
	isStreaming := handle.IsStreamingRequest()

	// If not streaming, use a simplified process flow with fewer updates
	if !isStreaming {
		log.Printf("Task %s using non-streaming mode", taskID)
		return p.processNonStreaming(ctx, taskID, text, handle)
	}

	// Continue with streaming process
	log.Printf("Task %s using streaming mode", taskID)

	// Update status to Working with an initial message
	initialMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart("Starting to process your streaming data...")},
	)
	if err := handle.UpdateStatus(protocol.TaskStateWorking, &initialMessage); err != nil {
		log.Printf("Error updating initial status for task %s: %v", taskID, err)
		return err
	}

	// Split the text into chunks to simulate streaming processing
	chunks := splitTextIntoChunks(text, 5) // Split into chunks of about 5 characters
	totalChunks := len(chunks)

	// Process each chunk with a small delay to simulate real-time processing
	for i, chunk := range chunks {
		// Check for cancellation
		if err := ctx.Err(); err != nil {
			log.Printf("Task %s cancelled during streaming: %v", taskID, err)
			_ = handle.UpdateStatus(protocol.TaskStateCanceled, nil)
			return err
		}

		// Process the chunk (in this example, just reverse it)
		processedChunk := reverseString(chunk)

		// Create a progress update message
		progressMsg := fmt.Sprintf("Processing chunk %d of %d: %s -> %s",
			i+1, totalChunks, chunk, processedChunk)
		statusMsg := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(progressMsg)},
		)

		// Update status to show progress
		if err := handle.UpdateStatus(protocol.TaskStateWorking, &statusMsg); err != nil {
			log.Printf("Error updating progress status for task %s: %v", taskID, err)
			// Continue processing despite update error
		}

		// Create an artifact for this chunk
		isLastChunk := (i == totalChunks-1)
		chunkArtifact := protocol.Artifact{
			Name:        stringPtr(fmt.Sprintf("Chunk %d of %d", i+1, totalChunks)),
			Description: stringPtr("Streaming chunk of processed data"),
			Index:       i,
			Parts:       []protocol.Part{protocol.NewTextPart(processedChunk)},
			Append:      boolPtr(i > 0),       // Append after the first chunk
			LastChunk:   boolPtr(isLastChunk), // Mark the last chunk
		}

		// Add the artifact
		if err := handle.AddArtifact(chunkArtifact); err != nil {
			log.Printf("Error adding artifact for task %s chunk %d: %v", taskID, i+1, err)
			// Continue processing despite artifact error
		}

		// Simulate processing time
		select {
		case <-ctx.Done():
			log.Printf("Task %s cancelled during delay: %v", taskID, ctx.Err())
			_ = handle.UpdateStatus(protocol.TaskStateCanceled, nil)
			return ctx.Err()
		case <-time.After(500 * time.Millisecond): // Simulate work with delay
			// Continue processing
		}
	}

	// Final completion status update
	completeMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{
			protocol.NewTextPart(
				fmt.Sprintf("Completed processing all %d chunks successfully!", totalChunks))},
	)
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, &completeMessage); err != nil {
		log.Printf("Error updating final status for task %s: %v", taskID, err)
		return fmt.Errorf("failed to update final task status: %w", err)
	}

	log.Printf("Task %s streaming completed successfully.", taskID)
	return nil
}

// processNonStreaming handles processing for non-streaming requests
// It processes the entire text at once and returns a single result
func (p *streamingTaskProcessor) processNonStreaming(
	ctx context.Context,
	taskID string,
	text string,
	handle taskmanager.TaskHandle,
) error {
	// Update status to Working with an initial message
	initialMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart("Processing your text...")},
	)
	if err := handle.UpdateStatus(protocol.TaskStateWorking, &initialMessage); err != nil {
		log.Printf("Error updating initial status for task %s: %v", taskID, err)
		return err
	}

	// Process the entire text at once
	processedText := reverseString(text)

	// Create a single artifact with the result
	artifact := protocol.Artifact{
		Name:        stringPtr("Processed Text"),
		Description: stringPtr("Complete processed text"),
		Index:       0,
		Parts:       []protocol.Part{protocol.NewTextPart(processedText)},
		LastChunk:   boolPtr(true),
	}

	// Add the artifact
	if err := handle.AddArtifact(artifact); err != nil {
		log.Printf("Error adding artifact for task %s: %v", taskID, err)
	}

	// Final completion status update
	completeMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{
			protocol.NewTextPart(
				fmt.Sprintf("Processing complete. Input: %s -> Output: %s", text, processedText))},
	)
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, &completeMessage); err != nil {
		log.Printf("Error updating final status for task %s: %v", taskID, err)
		return fmt.Errorf("failed to update final task status: %w", err)
	}

	log.Printf("Task %s non-streaming completed successfully.", taskID)
	return nil
}

// extractText extracts the first text part from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		// Type assert to the concrete TextPart type.
		if p, ok := part.(protocol.TextPart); ok {
			return p.Text
		}
	}
	return ""
}

// splitTextIntoChunks splits text into chunks of roughly the specified size.
// Ensures splits happen at word boundaries to avoid breaking words.
func splitTextIntoChunks(text string, chunkSize int) []string {
	// If text is short enough, return it as a single chunk
	if len(text) <= chunkSize {
		return []string{text}
	}

	// Split text by words to ensure we don't break words
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}

	chunks := []string{}
	currentChunk := ""

	for _, word := range words {
		// Check if adding this word would exceed the target chunk size
		if len(currentChunk) > 0 && len(currentChunk)+len(word)+1 > chunkSize && len(currentChunk) > 0 {
			// Current chunk is full, add it to the list
			chunks = append(chunks, currentChunk)
			currentChunk = word
		} else {
			// Add word to current chunk with a space if needed
			if len(currentChunk) > 0 {
				currentChunk += " "
			}
			currentChunk += word
		}
	}

	// Add the last chunk if not empty
	if len(currentChunk) > 0 {
		chunks = append(chunks, currentChunk)
	}

	// If we have very few chunks or they're very uneven, try a more balanced approach
	if len(chunks) < 3 && len(text) > 15 {
		// Find sentence boundaries or reasonable splitting points
		return splitAtSentenceBoundaries(text, 3)
	}

	return chunks
}

// splitAtSentenceBoundaries tries to split text at sentence boundaries or punctuation
// to create more natural chunks for streaming.
func splitAtSentenceBoundaries(text string, targetChunks int) []string {
	// Common sentence delimiters
	delimiters := []string{". ", "! ", "? ", "\n\n", "; "}

	// If text is small, don't try to split it too much
	if len(text) < 30 {
		return []string{text}
	}

	// Find all potential split points
	var splitPoints []int
	for _, delimiter := range delimiters {
		idx := 0
		for {
			found := strings.Index(text[idx:], delimiter)
			if found == -1 {
				break
			}
			// Add the position after the delimiter
			splitPoint := idx + found + len(delimiter)
			splitPoints = append(splitPoints, splitPoint)
			idx = splitPoint
		}
	}

	// Sort split points
	sort.Ints(splitPoints)

	// If no good split points found, fall back to even division
	if len(splitPoints) < targetChunks-1 {
		chunkSize := len(text) / targetChunks
		chunks := make([]string, targetChunks)
		for i := 0; i < targetChunks-1; i++ {
			chunks[i] = text[i*chunkSize : (i+1)*chunkSize]
		}
		chunks[targetChunks-1] = text[(targetChunks-1)*chunkSize:]
		return chunks
	}

	// Select evenly spaced split points
	selectedPoints := make([]int, targetChunks-1)
	step := len(splitPoints) / targetChunks
	for i := 0; i < targetChunks-1; i++ {
		index := min((i+1)*step, len(splitPoints)-1)
		selectedPoints[i] = splitPoints[index]
	}
	sort.Ints(selectedPoints)

	// Create chunks based on selected split points
	chunks := make([]string, targetChunks)
	startIdx := 0
	for i, point := range selectedPoints {
		chunks[i] = text[startIdx:point]
		startIdx = point
	}
	chunks[targetChunks-1] = text[startIdx:]

	return chunks
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	flag.IntVar(&port, "port", 8080, "Server port")
	flag.Parse()

	address := fmt.Sprintf("%s:%d", host, port)
	serverURL := fmt.Sprintf("http://%s/", address)

	// Create the agent card
	description := "A2A streaming example server that processes text in chunks"
	agentCard := server.AgentCard{
		Name:        "Streaming Text Processor",
		Description: &description,
		URL:         serverURL,
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Name: "tRPC-A2A-go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              true,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{string(protocol.PartTypeText)},
		DefaultOutputModes: []string{string(protocol.PartTypeText)},
		Skills: []server.AgentSkill{
			{
				ID:          "streaming_processor",
				Name:        "Streaming Text Processor",
				Description: stringPtr("Input: Any text\nOutput: Chunks of reversed text delivered incrementally\n\nExample input: hello world\nOutput chunk 1: oll\nOutput chunk 2: eh\nOutput chunk 3: dlrow"),
				Tags:        []string{"text", "stream", "example"},
				Examples: []string{
					"The quick brown fox jumps over the lazy dog",
					"Lorem ipsum dolor sit amet",
					"This demonstrates streaming capabilities",
				},
				InputModes:  []string{string(protocol.PartTypeText)},
				OutputModes: []string{string(protocol.PartTypeText)},
			},
		},
	}

	// Create the TaskProcessor (streaming logic)
	processor := &streamingTaskProcessor{}

	// Create the TaskManager, injecting the processor
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create the A2A server instance
	srv, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start the server in a goroutine
	go func() {
		log.Printf("Starting streaming server on %s...", address)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sig := <-sigChan
	log.Printf("Received signal %v, shutting down server...", sig)

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		log.Fatalf("Error during server shutdown: %v", err)
	}

	log.Println("Server shutdown complete")
}

// Helper functions to create pointers
func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}
