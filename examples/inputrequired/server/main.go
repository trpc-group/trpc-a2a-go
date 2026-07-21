// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates how an agent suspends a task for more input and
// resumes it when the client sends a follow-up message with the same task ID.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

type inputRequiredProcessor struct{}

func (*inputRequiredProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	if ec.Task == nil {
		// This is the first round. Emitting input-required creates the task,
		// suspends it, and ends this round. Do not wait here for user input.
		_ = handle.UpdateTaskState(
			protocol.TaskStateInputRequired,
			protocol.NewAgentText("What is your name?"),
		)
		return handle.Events(), nil
	}

	// A non-nil Task means that this message continues the suspended task.
	name := messageText(ec.Message)
	if name == "" {
		_ = handle.UpdateTaskState(
			protocol.TaskStateInputRequired,
			protocol.NewAgentText("Please provide a non-empty name."),
		)
		return handle.Events(), nil
	}

	_ = handle.UpdateTaskState(protocol.TaskStateWorking, nil)
	_ = handle.UpdateTaskState(
		protocol.TaskStateCompleted,
		protocol.NewAgentText(fmt.Sprintf("Nice to meet you, %s!", name)),
	)
	return handle.Events(), nil
}

func messageText(message protocol.Message) string {
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

func main() {
	host := flag.String("host", "localhost", "host to listen on")
	port := flag.Int("port", 8080, "port to listen on")
	flag.Parse()

	baseURL := fmt.Sprintf("http://%s:%d/", *host, *port)
	card := server.AgentCard{
		Name:               "Input Required Example",
		Description:        "Demonstrates suspending and continuing an A2A task",
		URL:                baseURL,
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Capabilities: server.AgentCapabilities{
			Streaming: boolPtr(false),
		},
		Skills: []server.AgentSkill{
			{
				ID:          "ask_name",
				Name:        "Ask for a name",
				Description: stringPtr("Requests missing input before completing"),
				Tags:        []string{"input-required", "multi-turn"},
			},
		},
	}

	manager, err := memory.NewTaskManager(&inputRequiredProcessor{})
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}
	srv, err := server.NewA2AServer(manager, server.WithAgentCard(card))
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	go func() {
		addr := fmt.Sprintf("%s:%d", *host, *port)
		log.Infof("Starting input-required example on %s", addr)
		if err := srv.Start(addr); err != nil {
			log.Fatalf("server failed: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
