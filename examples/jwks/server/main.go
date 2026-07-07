// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a server with push notification support using JWKS.
// The server demonstrates how to set up and use push notifications in an A2A server:
//
// 1. The JWKS endpoint and JWT authenticator are enabled via server options
// 2. The message processor runs tasks asynchronously and reports progress as events
// 3. When a task reaches a terminal state, the task manager sends a push notification
// 4. Notifications are signed using JWT with the private key in the authenticator
// 5. Clients can verify the notification using the public key from the JWKS endpoint
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/auth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const (
	defaultServerPort = 8000
)

// pushNotificationMessageProcessor is a message processor that sends push notifications.
// It implements the taskmanager.MessageProcessor interface for message processing
// and handles push notification functionality.
type pushNotificationMessageProcessor struct {
	manager *pushNotificationTaskManager
}

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
		taskmanager.ReplyText("Task queued for processing...")); err != nil {
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
		taskmanager.ReplyText(completeMsg)); err != nil {
		log.Errorf("Failed to send completed event: %v", err)
		return
	}

	// Send push notification
	p.manager.sendPushNotification(ctx, taskID, string(protocol.TaskStateCompleted))

	log.Infof("Task completed asynchronously: %s", taskID)
}

type pushNotificationTaskManager struct {
	taskmanager.TaskManager
	authenticator *auth.PushNotificationAuthenticator
}

// sendPushNotification sends a push notification for a completed task
func (m *pushNotificationTaskManager) sendPushNotification(ctx context.Context, taskID, status string) {
	log.Infof("Sending push notification for task: %s with status: %s", taskID, status)
	// Resolve the webhook the client registered for this task through the
	// TaskManager interface.
	pushConfig, err := m.TaskManager.OnPushNotificationGet(ctx, protocol.TaskIDParams{ID: taskID})
	if err != nil {
		log.Infof("No push notification configuration for task: %s", taskID)
		return
	}

	// Send push notification
	if err := m.authenticator.SendPushNotification(ctx, pushConfig.URL, map[string]interface{}{
		"task_id":   taskID,
		"status":    status,
		"timestamp": time.Now().Format(time.RFC3339),
	}); err != nil {
		log.Errorf("Failed to send push notification: %v", err)
	} else {
		log.Infof("Push notification sent successfully for task: %s", taskID)
	}
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
			PushNotifications:      boolPtr(true),
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

	authenticator := auth.NewPushNotificationAuthenticator()
	if err := authenticator.GenerateKeyPair(); err != nil {
		log.Fatalf("failed to generate key pair: %v", err)
	}

	// Create task processor
	processor := &pushNotificationMessageProcessor{}
	// Create task manager
	tm, err := memory.NewTaskManager(processor)
	if err != nil {
		log.Fatalf("failed to create task manager: %v", err)
	}

	// Create custom task manager with push notification support
	customTM := &pushNotificationTaskManager{
		TaskManager:   tm,
		authenticator: authenticator,
	}
	processor.manager = customTM
	// Combine standard options with additional options
	options := []server.Option{
		server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
		server.WithPushNotificationAuthenticator(authenticator),
	}

	// Create server with the authenticator
	a2aServer, err := server.NewA2AServer(
		customTM,
		append([]server.Option{server.WithAgentCard(agentCard)}, options...)...,
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
