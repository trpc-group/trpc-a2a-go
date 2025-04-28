// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a server with push notification support using JWKS.
// The server demonstrates how to set up and use push notifications in an A2A server:
//
// 1. Push notifications are enabled in the server factory via NewPushNotificationServer
// 2. The task processor implements the PushNotificationProcessor interface
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

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

const (
	defaultServerPort = 8000
	defaultNotifyHost = "localhost"
)

// pushNotificationTaskProcessor is a task processor that sends push notifications.
// It implements both the taskmanager.TaskProcessor interface for task processing
// and the PushNotificationProcessor interface for receiving the authenticator.
type pushNotificationTaskProcessor struct {
	notifyHost string
	manager    *pushNotificationTaskManager
}

// Process implements the TaskProcessor interface.
// This method should return quickly, only setting the task to "submitted" state
// and then processing the task asynchronously.
func (p *pushNotificationTaskProcessor) Process(
	ctx context.Context,
	taskID string,
	message protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	log.Infof("Task received: %s", taskID)

	// Extract task payload from the message parts
	var payload map[string]interface{}
	if len(message.Parts) > 0 {
		if textPart, ok := message.Parts[0].(protocol.TextPart); ok {
			if err := json.Unmarshal([]byte(textPart.Text), &payload); err != nil {
				log.Errorf("Failed to unmarshal payload text: %v", err)
				// Continue with empty payload
				payload = make(map[string]interface{})
			}
		}
	}

	// Update status to working
	if err := handle.UpdateStatus(protocol.TaskStateWorking, &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart("Task queued for processing..."),
		},
	}); err != nil {
		return fmt.Errorf("failed to update task status: %v", err)
	}

	// Start asynchronous processing
	go p.processTaskAsync(ctx, taskID, payload, handle)

	return nil
}

// OnTaskStatusUpdate implements the TaskProcessor interface.
func (p *pushNotificationTaskProcessor) OnTaskStatusUpdate(
	ctx context.Context,
	taskID string,
	state protocol.TaskState,
	message *protocol.Message,
) error {
	log.Infof("Updating task status for task: %s with status: %s", taskID, state)
	if state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed || state == protocol.TaskStateCanceled {
		p.manager.sendPushNotification(ctx, taskID, string(state))
	}
	return nil
}

// processTaskAsync handles the actual task processing in a separate goroutine.
func (p *pushNotificationTaskProcessor) processTaskAsync(
	ctx context.Context,
	taskID string,
	payload map[string]interface{},
	handle taskmanager.TaskHandle,
) {
	log.Infof("Starting async processing of task: %s", taskID)

	// Process the task (simulating work)
	time.Sleep(5 * time.Second) // Longer processing time to demonstrate async behavior

	// Prepare message for completion
	completeMsg := "Task completed"
	if content, ok := payload["content"].(string); ok {
		completeMsg = fmt.Sprintf("Task completed: %s", content)
	}

	// Complete the task
	// When we call UpdateStatus with a terminal state (like completed),
	// the task manager automatically:
	// 1. Updates the task status in memory
	// 2. Sends a push notification to the registered webhook URL (if enabled)
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(completeMsg),
		},
	}); err != nil {
		log.Errorf("Failed to update task status: %v", err)
		return
	}

	log.Infof("Task completed asynchronously: %s", taskID)
}

type pushNotificationTaskManager struct {
	taskmanager.TaskManager
	authenticator *auth.PushNotificationAuthenticator
}

func (m *pushNotificationTaskManager) OnSendTask(ctx context.Context, request protocol.SendTaskParams) (*protocol.Task, error) {
	task, err := m.TaskManager.OnSendTask(ctx, request)
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (m *pushNotificationTaskManager) sendPushNotification(ctx context.Context, taskID, status string) {
	log.Infof("Sending push notification for task: %s with status: %s", taskID, status)
	// Get push config from task manager
	ptm, ok := m.TaskManager.(*taskmanager.MemoryTaskManager)
	if !ok {
		log.Errorf("failed to cast task manager to memory task manager")
		return
	}
	pushConfig, exists := ptm.PushNotifications[taskID]
	if !exists {
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
		port       = flag.Int("port", defaultServerPort, "RPC port")
		notifyHost = flag.String("notify-host", defaultNotifyHost, "push notification host")
	)
	flag.Parse()

	// Create agent card
	agentCard := server.AgentCard{
		Name:        "Push Notification Example",
		Description: strPtr("A2A server example with push notification support"),
		URL:         fmt.Sprintf("http://localhost:%d/", *port),
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:              true,
			PushNotifications:      true,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	authenticator := auth.NewPushNotificationAuthenticator()
	if err := authenticator.GenerateKeyPair(); err != nil {
		log.Fatalf("failed to generate key pair: %v", err)
	}

	// Create task processor
	processor := &pushNotificationTaskProcessor{
		notifyHost: *notifyHost,
	}
	// Create task manager
	tm, err := taskmanager.NewMemoryTaskManager(processor)
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
		agentCard,
		customTM,
		options...,
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
