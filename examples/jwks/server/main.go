// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
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

	"github.com/google/uuid"
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

// pushNotificationMessageProcessor is a message processor that sends push notifications.
// It implements the taskmanager.MessageProcessor interface for message processing
// and handles push notification functionality.
type pushNotificationMessageProcessor struct {
	notifyHost string
	manager    *pushNotificationTaskManager
}

// ProcessMessage implements the MessageProcessor interface.
// This method processes messages and can handle both streaming and non-streaming modes.
func (p *pushNotificationMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	log.Infof("Message processing started")

	// Extract task payload from the message parts
	var payload map[string]interface{}
	var textContent string

	if len(message.Parts) > 0 {
		if textPart, ok := message.Parts[0].(*protocol.TextPart); ok {
			textContent = textPart.Text
			// Try to parse as JSON, but if it fails, treat as plain text
			if err := json.Unmarshal([]byte(textPart.Text), &payload); err != nil {
				log.Infof("Message content is plain text, not JSON: %s", textContent)
				// Create a simple payload with the text content
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

	// For non-streaming processing, return direct result
	if !options.Streaming {
		return p.processDirectly(ctx, payload)
	}

	// For streaming processing, create a task and process asynchronously
	taskID, err := handle.BuildTask(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}

	// Subscribe to the task for streaming events
	subscriber, err := handle.SubScribeTask(&taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to task: %w", err)
	}

	// Start asynchronous processing
	go p.processTaskAsync(ctx, taskID, payload, subscriber)

	return &taskmanager.MessageProcessingResult{
		StreamingEvents: subscriber,
	}, nil
}

// processDirectly handles immediate processing for non-streaming requests
func (p *pushNotificationMessageProcessor) processDirectly(
	ctx context.Context,
	payload map[string]interface{},
) (*taskmanager.MessageProcessingResult, error) {
	// Process the task immediately
	completeMsg := "Task completed"
	if content, ok := payload["content"].(string); ok {
		completeMsg = fmt.Sprintf("Task completed: %s", content)
	}

	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(completeMsg)},
	)

	return &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}, nil
}

// processTaskAsync handles the actual task processing in a separate goroutine.
func (p *pushNotificationMessageProcessor) processTaskAsync(
	ctx context.Context,
	taskID string,
	payload map[string]interface{},
	subscriber taskmanager.TaskSubscriber,
) {
	defer func() {
		if subscriber != nil {
			subscriber.Close()
		}
	}()

	log.Infof("Starting async processing of task: %s", taskID)

	// Send working status
	workingEvent := protocol.StreamingMessageEvent{
		Result: &protocol.TaskStatusUpdateEvent{
			TaskID:    taskID,
			ContextID: "", // We'll need to get this from the task
			Kind:      "status-update",
			Status: protocol.TaskStatus{
				State: protocol.TaskStateWorking,
				Message: &protocol.Message{
					MessageID: uuid.New().String(),
					Kind:      "message",
					Role:      protocol.MessageRoleAgent,
					Parts:     []protocol.Part{protocol.NewTextPart("Task queued for processing...")},
				},
			},
		},
	}
	err := subscriber.Send(workingEvent)
	if err != nil {
		log.Errorf("Failed to send working event: %v", err)
	}

	// Process the task (simulating work)
	time.Sleep(5 * time.Second) // Longer processing time to demonstrate async behavior

	// Prepare message for completion
	completeMsg := "Task completed"
	if content, ok := payload["content"].(string); ok {
		completeMsg = fmt.Sprintf("Task completed: %s", content)
	}

	// Send completion status
	completedEvent := protocol.StreamingMessageEvent{
		Result: &protocol.TaskStatusUpdateEvent{
			TaskID:    taskID,
			ContextID: "", // We'll need to get this from the task
			Kind:      "status-update",
			Status: protocol.TaskStatus{
				State: protocol.TaskStateCompleted,
				Message: &protocol.Message{
					MessageID: uuid.New().String(),
					Kind:      "message",
					Role:      protocol.MessageRoleAgent,
					Parts:     []protocol.Part{protocol.NewTextPart(completeMsg)},
				},
			},
			Final: true,
		},
	}
	err = subscriber.Send(completedEvent)
	if err != nil {
		log.Errorf("Failed to send completed event: %v", err)
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
	if err := m.authenticator.SendPushNotification(ctx, pushConfig.PushNotificationConfig.URL, map[string]interface{}{
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
	processor := &pushNotificationMessageProcessor{
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

// Helper function to create bool pointer
func boolPtr(b bool) *bool {
	return &b
}
