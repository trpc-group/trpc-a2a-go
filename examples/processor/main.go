// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates the util.Processor style: the agent body is
// written straight-line — no goroutine, no Events(), no manual Close — and
// util.AsMessageProcessor bridges it onto the standard MessageProcessor
// contract. One code path serves message/send and message/stream alike.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/examples/util"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// chunkProcessor uppercases the input in chunks, emitting progress as it goes.
// Compare with examples/jwks, which drives the same kind of asynchronous round
// directly on the MessageProcessor contract (handle + goroutine + Close).
type chunkProcessor struct{}

// Process is the whole agent body: emit, work, emit, return.
func (p *chunkProcessor) Process(
	ctx context.Context,
	ec *taskmanager.ExecContext,
	h *taskmanager.TaskHandle,
) error {
	text := extractText(ec.Message)
	if text == "" {
		// A pure-message reply: no task comes into existence this round.
		return h.Reply(protocol.NewAgentText("input message must contain text"))
	}

	// First task event: the framework creates the task lazily and stamps IDs.
	if err := h.UpdateTaskState(protocol.TaskStateWorking,
		protocol.NewAgentText("processing...")); err != nil {
		return err
	}

	chunks := splitIntoChunks(strings.ToUpper(text), 8)
	for i, chunk := range chunks {
		// Returning after a cancel, without a terminal state, lets the
		// framework persist CANCELED on our behalf.
		select {
		case <-ctx.Done():
			log.Infof("Task %s canceled at chunk %d/%d", h.TaskID(), i+1, len(chunks))
			return nil
		case <-time.After(500 * time.Millisecond): // simulate work
		}

		if err := h.AddArtifact(*protocol.NewArtifactWithID(
			stringPtr(fmt.Sprintf("Chunk %d of %d", i+1, len(chunks))),
			stringPtr("Uppercased chunk"),
			[]*protocol.Part{protocol.NewTextPart(chunk)},
		), i == len(chunks)-1); err != nil {
			return err
		}
	}

	// Terminal state, then return: the adapter ends the round.
	return h.UpdateTaskState(protocol.TaskStateCompleted,
		protocol.NewAgentText(fmt.Sprintf("Uppercased %d chunks: %s", len(chunks), strings.ToUpper(text))))
}

// extractText extracts the first text content from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}

// splitIntoChunks splits s into pieces of at most size bytes.
func splitIntoChunks(s string, size int) []string {
	if size <= 0 || len(s) <= size {
		return []string{s}
	}
	var chunks []string
	for len(s) > size {
		chunks = append(chunks, s[:size])
		s = s[size:]
	}
	if len(s) > 0 {
		chunks = append(chunks, s)
	}
	return chunks
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

func main() {
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8090, "Port to listen on")
	flag.Parse()

	agentCard := server.AgentCard{
		Name:        "Processor Style Example",
		Description: "Uppercases text in chunks; the agent body is a straight-line Processor",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
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
				ID:          "chunked_uppercase",
				Name:        "Chunked Uppercase",
				Description: stringPtr("Uppercases the input text, streamed in chunks"),
				Tags:        []string{"text", "example"},
				Examples:    []string{"hello world"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// The only wiring difference from the other examples: wrap the Processor
	// with util.AsMessageProcessor before handing it to the task manager.
	taskManager, err := memory.NewTaskManager(util.AsMessageProcessor(&chunkProcessor{}))
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		serverAddr := fmt.Sprintf("%s:%d", *host, *port)
		log.Infof("Starting processor-style server on %s...", serverAddr)
		if err := srv.Start(serverAddr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)
}
