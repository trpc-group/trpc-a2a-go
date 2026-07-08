// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements an A2A server that also speaks the legacy v0.2.x
// wire protocol.
//
// The agent itself is written once, on the v2 MessageProcessor contract. The
// compat/v0 handler is mounted on the same JSON-RPC endpoint via
// server.WithCompatHandler: legacy method names (message/send, tasks/get,
// tasks/resubscribe, ...) are disjoint from the v1.0 names, so both client
// generations are served side by side — through the same authentication
// chain, by the same TaskManager. Use this setup to keep existing v0.x
// clients working while they migrate.
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

	v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// reverseProcessor reverses the input text as a staged task round: working,
// then (after a short simulated delay) the artifact and the completed state.
// The delay is deliberate — it lets the legacy client observe the v0.2.x
// non-blocking default: a configuration-less legacy message/send returns the
// working snapshot immediately while the round keeps running.
type reverseProcessor struct{}

// ProcessMessage implements taskmanager.MessageProcessor.
func (p *reverseProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)

	text := extractText(ec.Message)
	if text == "" {
		// A pure message reply: no task comes into existence this round.
		handle.Reply(protocol.NewAgentText("input message must contain text"))
		handle.Close()
		return handle.Events(), nil
	}

	// The working snapshot is emitted before returning: it answers the
	// legacy non-blocking (and v1 returnImmediately) callers.
	handle.UpdateTaskState(protocol.TaskStateWorking, nil)

	go func() {
		defer handle.Close()

		// Simulate work so the non-blocking demo has something to poll.
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			// Canceled: stop emitting and close — the framework persists
			// the CANCELED state.
			return
		}

		result := reverseString(text)
		handle.AddArtifact(protocol.Artifact{
			ArtifactID: "reversed-" + handle.TaskID(),
			Name:       stringPtr("Reversed Text"),
			Parts:      []*protocol.Part{protocol.NewTextPart(result)},
		}, true)
		handle.UpdateTaskState(protocol.TaskStateCompleted,
			protocol.NewAgentText(fmt.Sprintf("Reversed: %s", result)))
	}()
	return handle.Events(), nil
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	var texts []string
	for _, part := range message.Parts {
		if t := part.TextContent(); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, " ")
}

// reverseString reverses a string.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// stringPtr returns a pointer to the given string.
func stringPtr(s string) *string {
	return &s
}

func main() {
	var (
		host string
		port int
	)
	flag.StringVar(&host, "host", "localhost", "Server host address")
	flag.IntVar(&port, "port", 8080, "Server port")
	flag.Parse()

	address := fmt.Sprintf("%s:%d", host, port)
	serverURL := fmt.Sprintf("http://%s/", address)

	agentCard := server.AgentCard{
		Name:        "Text Reversal Agent (v0-compatible)",
		Description: "Reverses text; serves both the v1.0 and the legacy v0.2.x wire protocol",
		URL:         serverURL,
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_reverse",
				Name:        "Text Reverser",
				Description: stringPtr("Input: reverse hello\nOutput: olleh"),
				Tags:        []string{"text", "reverse"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	tm, err := memory.NewTaskManager(&reverseProcessor{})
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// One TaskManager, two wire protocols: the compat handler serves the
	// legacy v0.2.x method names on the same endpoint, inside the same
	// authentication chain as the v1.0 path.
	srv, err := server.NewA2AServer(tm,
		server.WithAgentCard(agentCard),
		server.WithCompatHandler(v0.NewJSONRPCHandler(tm)),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Infof("v0-compatible agent server starting on %s", address)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	<-sigChan
	log.Info("Shutdown signal received...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		log.Errorf("Server shutdown failed: %v", err)
	}
}

// boolPtr returns a pointer to the given bool.
func boolPtr(b bool) *bool {
	return &b
}
