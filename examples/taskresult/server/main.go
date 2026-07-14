// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main runs an A2A server whose work takes a little time, so a client
// can send a message, get the task id back immediately (returnImmediately), and
// then collect the result later via tasks/get (polling) or tasks/resubscribe
// (streaming). The processing logic is a mock agent (examples/util).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/examples/util"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// newAgent builds a mock agent that takes a couple of seconds to finish, so the
// send-then-collect flow is observable. It uppercases the input and streams it
// back in chunks with a delay between them.
func newAgent() util.Agent {
	return util.NewMockAgent(
		func(input string) []string {
			return util.Chunk(strings.ToUpper(input), 8)
		},
		util.WithChunkDelay(500*time.Millisecond),
	)
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

func main() {
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8082, "Port to listen on")
	flag.Parse()

	agentCard := server.AgentCard{
		Name:        "Task Result Example Server",
		Description: "Runs a slow task so clients can fetch its result via tasks/get or tasks/resubscribe",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
			URL:          stringPtr(fmt.Sprintf("http://%s:%d/", *host, *port)),
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
				ID:          "slow_uppercase",
				Name:        "Slow Uppercase",
				Description: stringPtr("Uppercases text over a few seconds"),
				Tags:        []string{"text", "async"},
				Examples:    []string{"hello world"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	taskManager, err := memory.NewTaskManager(util.NewMessageProcessor(newAgent()))
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
		log.Infof("Starting task-result server on %s...", serverAddr)
		if err := srv.Start(serverAddr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)
}
