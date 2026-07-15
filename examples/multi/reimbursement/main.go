// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the reimbursement sub-agent in the multi-agent example.
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

type reimbursementProcessor struct{}

func (p *reimbursementProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	request := textFromMessage(ec.Message)
	if request == "" {
		_ = handle.Reply(protocol.NewAgentText("input message must contain text"))
		return handle.Events(), nil
	}

	response := fmt.Sprintf(
		"Reimbursement request received: %q. A production agent would validate and persist it here.",
		request,
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
	port := flag.Int("port", 8083, "port to listen on")
	flag.Parse()

	taskManager, err := memory.NewTaskManager(&reimbursementProcessor{})
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}

	address := fmt.Sprintf("%s:%d", *host, *port)
	agentURL := "http://" + address + "/"
	streaming := false
	pushNotifications := false
	description := "Acknowledges a reimbursement request without external services"
	agentCard := protocol.AgentCard{
		Name:        "Reimbursement Agent",
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
			ID:          "reimbursement",
			Name:        "Reimbursement",
			Description: &description,
			Tags:        []string{"expense", "reimbursement"},
			Examples:    []string{"Submit an expense receipt"},
		}},
	}

	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	go func() {
		log.Printf("reimbursement agent listening at %s", agentURL)
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
