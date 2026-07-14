// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements an A2A server with automatic push notifications + JWKS.
//
//  1. A push.Sender is wired into the TaskManager via memory.WithPushNotifications,
//     so the framework delivers task updates to registered webhooks automatically —
//     the message processor never sends notifications itself.
//  2. The sender signs each callback with a JWT (via its signer); the server
//     publishes the verification keys at a JWKS endpoint.
//  3. On a terminal state the framework POSTs a StreamResponse to every webhook the
//     client registered; the client verifies the JWT using the JWKS public key.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const (
	defaultServerPort = 8000
)

// pushNotificationMessageProcessor runs tasks asynchronously and reports state.
// It does NOT send push notifications itself — the framework delivers them
// automatically once a push.Sender is wired into the TaskManager (see main).
type pushNotificationMessageProcessor struct{}

// ProcessMessage implements the taskmanager.MessageProcessor interface.
// One code path serves message/send and message/stream alike: the task is
// moved to working right away and completed asynchronously, so unary clients
// opt into returnImmediately=true to learn the task ID and register their
// webhook while the task is still running.
func (p *pushNotificationMessageProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	log.Infof("Message processing started")

	// Extract task payload from the message parts
	var payload map[string]interface{}
	var textContent string

	if len(ec.Message.Parts) > 0 {
		textContent = ec.Message.Parts[0].TextContent()
		if textContent != "" {
			if err := json.Unmarshal([]byte(textContent), &payload); err != nil {
				log.Infof("Message content is plain text, not JSON: %s", textContent)
				payload = map[string]interface{}{
					"content": textContent,
					"type":    "text",
				}
			} else {
				log.Infof("Message content parsed as JSON successfully")
			}
		}
	}

	if payload == nil {
		payload = map[string]interface{}{
			"content": "empty message",
			"type":    "text",
		}
	}

	handle := taskmanager.NewTaskHandle(ctx, ec)

	// Move the task to working before returning: this persisted snapshot
	// answers a returnImmediately unary call while processing continues below.
	if err := handle.UpdateTaskState(protocol.TaskStateWorking,
		protocol.NewAgentText("Task queued for processing...")); err != nil {
		log.Errorf("Failed to send working event: %v", err)
	}

	// Start asynchronous processing: the goroutine emits the terminal event
	// and closes the round.
	go p.processTaskAsync(ctx, handle, payload)

	return handle.Events(), nil
}

// processTaskAsync handles the actual task processing in a separate goroutine.
func (p *pushNotificationMessageProcessor) processTaskAsync(
	ctx context.Context,
	handle *taskmanager.TaskHandle,
	payload map[string]interface{},
) {
	// Closing the round with the task completed ends the stream normally.
	defer handle.Close()

	taskID := handle.TaskID()
	log.Infof("Starting async processing of task: %s", taskID)

	// Respect cancellation across the simulated work: closing without a
	// terminal state lets the framework persist CANCELED — and no completed
	// push fires for a canceled task.
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		log.Infof("Task %s canceled during processing", taskID)
		return
	}

	completeMsg := "Task completed"
	if content, ok := payload["content"].(string); ok {
		completeMsg = fmt.Sprintf("Task completed: %s", content)
	}

	if err := handle.UpdateTaskState(protocol.TaskStateCompleted,
		protocol.NewAgentText(completeMsg)); err != nil {
		log.Errorf("Failed to send completed event: %v", err)
		return
	}

	// The framework auto-delivers a push notification for this terminal state to
	// every webhook the client registered — no manual send needed here.
	log.Infof("Task completed asynchronously: %s", taskID)
}

func main() {
	// Parse command line flags
	var (
		port = flag.Int("port", defaultServerPort, "RPC port")
	)
	flag.Parse()

	// Create agent card
	agentCard := server.AgentCard{
		Name:        "Push Notification Example",
		Description: "A2A server example with push notification support",
		URL:         fmt.Sprintf("http://localhost:%d/", *port),
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "push_notification_task",
				Name:        "Push Notification Task",
				Description: strPtr("Processes tasks with push notification support"),
				Tags:        []string{"push", "notification", "async"},
				Examples:    []string{`{"content": "Hello, world!"}`},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// A SignedSender generates a signing key by default. Production replicas can
	// share one key via pushauth.WithJWTKey so every instance signs with a key
	// published through JWKS.
	// This local demo intentionally posts to a loopback client webhook.
	signedSender, err := pushauth.NewSignedSender(
		pushauth.WithSenderOptions(push.WithUnsafeAllowPrivateNetworks()))
	if err != nil {
		log.Fatalf("failed to create signed push sender: %v", err)
	}

	// TaskManager: automatic delivery for every task event.
	processor := &pushNotificationMessageProcessor{}
	tm, err := memory.NewTaskManager(processor,
		memory.WithPushNotifications(signedSender),
	)
	if err != nil {
		log.Fatalf("failed to create task manager: %v", err)
	}

	// Server: publish the sender's verification keys at the standard JWKS path.
	a2aServer, err := server.NewA2AServer(tm,
		server.WithAgentCard(agentCard),
		server.WithPushNotificationJWKSHandler(signedSender.JWKSHandler()),
	)
	if err != nil {
		log.Fatalf("failed to create A2A server: %v", err)
	}

	// Start the server
	log.Infof("Starting A2A server on port %d...", *port)
	if err := a2aServer.Start(fmt.Sprintf(":%d", *port)); err != nil {
		log.Fatalf("Failed to start A2A server: %v", err)
	}
}

// Helper function to create string pointer
func strPtr(s string) *string {
	return &s
}

// Helper function to create bool pointer
func boolPtr(b bool) *bool {
	return &b
}
