// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a client demonstrating how to receive and verify
// push notifications using JWKS.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

const (
	defaultTaskQueueName = "task-queue"
	defaultServerHost    = "localhost"
	defaultServerPort    = 8000
	defaultWebhookPort   = 8001
	defaultWebhookHost   = "localhost"
	defaultWebhookPath   = "/webhook"
)

// Config holds client configuration.
type Config struct {
	ServerHost   string
	ServerPort   int
	WebhookHost  string
	WebhookPort  int
	WebhookPath  string
	JWKSEndpoint string
}

// TaskStatusChange represents a push notification for a task status update.
type TaskStatusChange struct {
	TaskID    string          `json:"task_id"`
	Status    string          `json:"status"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message,omitempty"`
}

// WebhookHandler handles incoming push notifications.
type WebhookHandler struct {
	keyset      jwk.Set
	keysetMutex sync.RWMutex
	jwksURL     string
}

// NewWebhookHandler creates a new webhook handler for push notifications.
func NewWebhookHandler(jwksURL string) *WebhookHandler {
	handler := &WebhookHandler{
		jwksURL: jwksURL,
	}

	// Fetch JWKS initially
	if err := handler.refreshJWKS(); err != nil {
		log.Errorf("Initial JWKS fetch failed: %v", err)
	} else {
		log.Infof("Initial JWKS fetch successful")
	}

	// Start a background goroutine to refresh JWKS periodically
	go handler.periodicJWKSRefresh()

	return handler
}

// periodicJWKSRefresh refreshes the JWKS every 15 minutes.
func (h *WebhookHandler) periodicJWKSRefresh() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if err := h.refreshJWKS(); err != nil {
			log.Errorf("JWKS refresh failed: %v", err)
		} else {
			log.Infof("JWKS refreshed successfully")
		}
	}
}

// refreshJWKS fetches the latest JWKS from the server.
func (h *WebhookHandler) refreshJWKS() error {
	log.Infof("Fetching JWKS from %s", h.jwksURL)

	// Create HTTP client with timeout
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Fetch JWKS
	resp, err := httpClient.Get(h.jwksURL)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch JWKS: HTTP %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read JWKS response: %w", err)
	}

	// Parse JWKS
	keyset, err := jwk.Parse(body)
	if err != nil {
		return fmt.Errorf("failed to parse JWKS: %w", err)
	}

	// Update keyset
	h.keysetMutex.Lock()
	h.keyset = keyset
	h.keysetMutex.Unlock()

	log.Infof("JWKS updated successfully, contains %d keys", keyset.Len())
	return nil
}

// ServeHTTP handles incoming push notifications from the server.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Infof("Received push notification")

	// Check if this is a POST request
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get JWT from Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		log.Infof("No Authorization header found")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract token from header (Bearer <token>)
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		log.Infof("Invalid Authorization header format")
		http.Error(w, "Invalid Authorization format", http.StatusUnauthorized)
		return
	}
	tokenString := parts[1]

	// Verify JWT signature using JWKS
	token, err := h.verifyJWT(tokenString)
	if err != nil {
		log.Infof("JWT verification failed: %v", err)
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Infof("Failed to read request body: %v", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Parse notification
	var notification map[string]interface{}
	if err := json.Unmarshal(body, &notification); err != nil {
		log.Infof("Failed to parse notification: %v", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Extract and validate task ID from payload
	taskID, ok := notification["task_id"].(string)
	if !ok {
		log.Infof("Notification missing task_id field")
		http.Error(w, "Invalid notification format", http.StatusBadRequest)
		return
	}

	// Extract task ID from JWT payload
	jwtPayload, err := token.AsMap(context.Background())
	if err != nil {
		log.Infof("Failed to extract JWT payload: %v", err)
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	payloadObj, ok := jwtPayload["payload"].(map[string]interface{})
	if !ok {
		log.Infof("JWT missing payload field")
		http.Error(w, "Invalid token format", http.StatusUnauthorized)
		return
	}

	jwtTaskID, ok := payloadObj["task_id"].(string)
	if !ok || jwtTaskID != taskID {
		log.Infof("Task ID mismatch: JWT=%v vs Notification=%s", jwtTaskID, taskID)
		http.Error(w, "Task ID mismatch", http.StatusUnauthorized)
		return
	}

	// Log verification success
	status, _ := notification["status"].(string)
	log.Infof("Verified notification for task %s: Status = %s", taskID, status)
	prettyJSON, _ := json.MarshalIndent(notification, "", "  ")
	log.Infof("Notification details: %s", string(prettyJSON))

	// Respond with success
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Notification received and verified"))
}

// verifyJWT verifies a JWT token using the JWKS.
func (h *WebhookHandler) verifyJWT(tokenString string) (jwt.Token, error) {
	// Get keyset for verification
	h.keysetMutex.RLock()
	keyset := h.keyset
	h.keysetMutex.RUnlock()

	if keyset == nil {
		return nil, fmt.Errorf("no JWKS available")
	}

	// Verify token
	return jwt.Parse(
		[]byte(tokenString),
		jwt.WithKeySet(keyset),
		jwt.WithValidate(true),
	)
}

// startWebhookServer starts the webhook server on the specified host and port.
func startWebhookServer(cfg *Config, handler http.Handler) {
	addr := fmt.Sprintf("%s:%d", cfg.WebhookHost, cfg.WebhookPort)
	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Route all webhook path requests to the handler
			if r.URL.Path == cfg.WebhookPath {
				handler.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		}),
	}

	log.Infof("Starting webhook server on %s", addr)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Webhook server failed: %v", err)
		}
	}()
}

func main() {
	// Parse command line flags
	cfg := Config{
		ServerHost:  defaultServerHost,
		ServerPort:  defaultServerPort,
		WebhookHost: defaultWebhookHost,
		WebhookPort: defaultWebhookPort,
		WebhookPath: defaultWebhookPath,
	}

	flag.StringVar(&cfg.ServerHost, "server-host", defaultServerHost, "A2A server host")
	flag.IntVar(&cfg.ServerPort, "server-port", defaultServerPort, "A2A server port")
	flag.StringVar(&cfg.WebhookHost, "webhook-host", defaultWebhookHost, "Webhook server host")
	flag.IntVar(&cfg.WebhookPort, "webhook-port", defaultWebhookPort, "Webhook server port")
	flag.StringVar(&cfg.WebhookPath, "webhook-path", defaultWebhookPath, "Webhook endpoint path")
	flag.Parse()

	// Set JWKS endpoint URL
	cfg.JWKSEndpoint = fmt.Sprintf("http://%s:%d/.well-known/jwks.json",
		cfg.ServerHost, cfg.ServerPort)

	// Construct webhook URL
	webhookURL := fmt.Sprintf("http://%s:%d%s",
		cfg.WebhookHost, cfg.WebhookPort, cfg.WebhookPath)

	// Create webhook handler
	handler := NewWebhookHandler(cfg.JWKSEndpoint)

	// Start webhook server
	startWebhookServer(&cfg, handler)

	// Create A2A client
	a2aClient, err := client.NewA2AClient(
		fmt.Sprintf("http://%s:%d/", cfg.ServerHost, cfg.ServerPort),
		client.WithTimeout(30*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	// Generate task ID
	taskID := fmt.Sprintf("task-%d", time.Now().Unix())
	log.Infof("Task ID: %s", taskID)

	// Create task payload
	payload := map[string]interface{}{
		"content": "Test task with push notification",
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("Failed to marshal payload: %v", err)
	}

	// Create task parameters
	params := protocol.SendTaskParams{
		ID: taskID,
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]protocol.Part{
				protocol.NewTextPart(string(payloadBytes)),
			},
		),
	}

	// Set up push notification configuration BEFORE sending the task
	// This prevents a race condition where the task completes before notification is registered
	pushConfig := protocol.TaskPushNotificationConfig{
		ID: taskID,
		PushNotificationConfig: protocol.PushNotificationConfig{
			URL: webhookURL,
			// JWT authentication will be automatically set up by the server
		},
	}

	// Register for push notifications FIRST
	log.Infof("Registering for push notifications at: %s", webhookURL)
	_, err = a2aClient.SetPushNotification(context.Background(), pushConfig)
	if err != nil {
		log.Fatalf("Failed to set push notification: %v", err)
	}
	log.Infof("Successfully registered for push notifications")

	// Now send the task
	log.Infof("Sending task to %s:%d...", cfg.ServerHost, cfg.ServerPort)
	task, err := a2aClient.SendTasks(context.Background(), params)
	if err != nil {
		log.Fatalf("Failed to send task: %v", err)
	}
	log.Infof("Task sent successfully, initial status: %s", task.Status.State)

	log.Infof("Waiting for push notifications...")

	// Wait for termination signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Infof("Shutting down...")
}
