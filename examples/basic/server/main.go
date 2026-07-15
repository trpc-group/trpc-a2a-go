// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the server for the basic A2A task lifecycle example.
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

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

type basicProcessor struct{}

func (p *basicProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	// A continuation is identified by the framework-provided task snapshot.
	// No application-side context or session map is needed.
	if ec.Task != nil {
		return completeContinuation(ctx, ec), nil
	}

	text := extractText(ec.Message)
	if text == "wait" {
		return runUntilCanceled(ctx, ec), nil
	}

	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	if text == "" {
		_ = handle.Reply(protocol.NewAgentText("input message must contain text"))
		return handle.Events(), nil
	}
	if text == "profile" {
		_ = handle.UpdateTaskState(
			protocol.TaskStateInputRequired,
			protocol.NewAgentText("What name should I use for your profile?"),
		)
		return handle.Events(), nil
	}

	result := strings.ToUpper(text)
	_ = handle.UpdateTaskState(protocol.TaskStateWorking, nil)
	_ = handle.AddArtifact(protocol.Artifact{
		ArtifactID: "uppercase-" + handle.TaskID(),
		Parts:      []*protocol.Part{protocol.NewTextPart(result)},
	}, true)
	_ = handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(result))
	return handle.Events(), nil
}

func completeContinuation(ctx context.Context, ec *taskmanager.ExecContext) <-chan protocol.StreamEvent {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	name := extractText(ec.Message)
	result := fmt.Sprintf("Hello, %s. Task %s is complete.", name, ec.Task.ID)
	_ = handle.UpdateTaskState(protocol.TaskStateWorking, nil)
	_ = handle.AddArtifact(protocol.Artifact{
		ArtifactID: "profile-" + handle.TaskID(),
		Parts:      []*protocol.Part{protocol.NewTextPart(result)},
	}, true)
	_ = handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(result))
	return handle.Events()
}

// runUntilCanceled keeps a task live so the client can demonstrate tasks/cancel.
// When cancellation closes ctx, the processor ends without a terminal event and
// the task manager persists the framework-generated CANCELED state.
func runUntilCanceled(ctx context.Context, ec *taskmanager.ExecContext) <-chan protocol.StreamEvent {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	out := handle.Events()
	go func() {
		defer handle.Close()
		_ = handle.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText("Waiting for cancellation"))

		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("Wait finished"))
		}
	}()
	return out
}

func extractText(message protocol.Message) string {
	var texts []string
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.TrimSpace(strings.Join(texts, " "))
}

func boolPtr(value bool) *bool { return &value }

func main() {
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	endpoint := fmt.Sprintf("http://%s:%d/", *host, *port)
	card := protocol.AgentCard{
		Name:        "Basic Task Lifecycle Agent",
		Description: "Demonstrates v2 task creation, continuation, query, list, and cancellation",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             endpoint,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Version: "2.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:       "task-lifecycle",
			Name:     "Task lifecycle",
			Tags:     []string{"tasks", "continuation", "cancellation"},
			Examples: []string{"hello", "profile", "wait"},
		}},
	}

	manager, err := memory.NewTaskManager(&basicProcessor{})
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}
	srv, err := server.NewA2AServer(manager, server.WithAgentCard(card))
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	address := fmt.Sprintf("%s:%d", *host, *port)
	go func() {
		log.Infof("Basic task lifecycle agent listening on %s", address)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		log.Errorf("Server shutdown failed: %v", err)
	}
}
