// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the creative sub-agent in the multi-agent example.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

type creativeProcessor struct{}

func (p *creativeProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	prompt := textFromMessage(ec.Message)
	if prompt == "" {
		_ = handle.Reply(protocol.NewAgentText("input message must contain text"))
		return handle.Events(), nil
	}

	response := fmt.Sprintf(
		"Creative draft for %q:\nA small idea crossed the wire, found a new voice, and returned as a story.",
		prompt,
	)
	_ = handle.Reply(protocol.NewAgentText(response))
	return handle.Events(), nil
}

func textFromMessage(message protocol.Message) string {
	var result strings.Builder
	for _, part := range message.Parts {
		result.WriteString(part.TextContent())
	}
	return strings.TrimSpace(result.String())
}

func main() {
	host := flag.String("host", "localhost", "host to listen on")
	port := flag.Int("port", 8082, "port to listen on")
	flag.Parse()

	taskManager, err := memory.NewTaskManager(&creativeProcessor{})
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}

	address := fmt.Sprintf("%s:%d", *host, *port)
	agentURL := "http://" + address + "/"
	streaming := false
	pushNotifications := false
	description := "Returns a deterministic creative-writing sample"
	agentCard := protocol.AgentCard{
		Name:        "Creative Writing Agent",
		Description: "Deterministic sub-agent used to demonstrate A2A orchestration",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             agentURL,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Version: "2.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:         &streaming,
			PushNotifications: &pushNotifications,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:          "creative-writing",
			Name:        "Creative writing",
			Description: &description,
			Tags:        []string{"creative", "writing"},
			Examples:    []string{"Write a short story about a space explorer"},
		}},
	}

	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	go func() {
		log.Printf("creative agent listening at %s", agentURL)
		if err := srv.Start(address); err != nil {
			log.Printf("server stopped: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		log.Printf("stop server: %v", err)
	}
}
