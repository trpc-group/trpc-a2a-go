// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides a Redis-based implementation of the A2A TaskManager interface.
package redis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// getPushNotificationConfig retrieves a push notification configuration for a task.
func (m *TaskManager) getPushNotificationConfig(
	ctx context.Context, taskID string,
) (*protocol.PushNotificationConfig, error) {
	key := pushNotificationPrefix + taskID
	val, err := m.client.Get(ctx, key).Result()
	if err != nil {
		if err.Error() == "redis: nil" {
			// No push notification configured for this task.
			return nil, nil
		}
		return nil, err
	}
	var config protocol.PushNotificationConfig
	if err := json.Unmarshal([]byte(val), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal push notification config: %w", err)
	}
	return &config, nil
}

// sendPushNotification sends a notification to the registered webhook URL.
func (m *TaskManager) sendPushNotification(
	ctx context.Context, taskID string, event protocol.TaskEvent,
) error {
	// Get the notification config.
	config, err := m.getPushNotificationConfig(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get push notification config: %w", err)
	}
	if config == nil {
		// No push notification configured, nothing to do.
		return nil
	}

	// Prepare the notification payload.
	eventType := ""
	if _, isStatus := event.(protocol.TaskStatusUpdateEvent); isStatus {
		eventType = protocol.EventTaskStatusUpdate
	} else if _, isArtifact := event.(protocol.TaskArtifactUpdateEvent); isArtifact {
		eventType = protocol.EventTaskArtifactUpdate
	} else {
		return fmt.Errorf("unsupported event type: %T", event)
	}

	notification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tasks/notifyEvent",
		"params": map[string]interface{}{
			"id":        taskID,
			"eventType": eventType,
			"event":     event,
		},
	}

	// Marshal the notification to JSON.
	body, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	// Create HTTP request.
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, config.URL, bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("failed to create notification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Add authentication if configured.
	if config.Authentication != nil {
		// Check for JWT authentication using the "bearer" scheme.
		for _, scheme := range config.Authentication.Schemes {
			if strings.EqualFold(scheme, "bearer") {
				// Check if we have a JWKs URL in the metadata for JWT auth
				if config.Metadata != nil {
					if jwksURL, ok := config.Metadata["jwksUrl"].(string); ok && jwksURL != "" {
						// Create a JWT token for the notification
						if authHeader, err := m.createJWTAuthHeader(body, jwksURL); err == nil {
							req.Header.Set("Authorization", authHeader)
							break
						} else {
							log.Errorf("Failed to create JWT auth header: %v", err)
						}
					}
				}
			}
		}

		// Add token if provided.
		if config.Token != "" {
			req.Header.Set("Authorization", "Bearer "+config.Token)
		}
	}

	// Send the notification.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	defer resp.Body.Close()

	// Check for success.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("notification failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// createJWTAuthHeader creates a JWT authorization header for push notifications.
// It uses the jwksURL to configure a client to fetch the JWKs when needed.
func (m *TaskManager) createJWTAuthHeader(payload []byte, jwksURL string) (string, error) {
	// Initialize the push auth helper if needed
	m.pushAuthMu.Lock()
	defer m.pushAuthMu.Unlock()

	if m.pushAuth == nil {
		// This is the first request, so initialize the push auth helper
		if err := m.initializePushAuth(jwksURL); err != nil {
			return "", err
		}
	}

	// Create the authorization header
	return m.pushAuth.CreateAuthorizationHeader(payload)
}

// Initialize the push notification authenticator.
func (m *TaskManager) initializePushAuth(jwksURL string) error {
	// Create a new authenticator and generate a key pair
	m.pushAuth = auth.NewPushNotificationAuthenticator()
	if err := m.pushAuth.GenerateKeyPair(); err != nil {
		m.pushAuth = nil
		return fmt.Errorf("failed to generate key pair: %w", err)
	}

	return nil
}
