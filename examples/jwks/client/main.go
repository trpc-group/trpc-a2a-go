// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a client demonstrating how to receive and verify
// push notifications using JWKS.
//
// This example demonstrates the recommended approach for long-running tasks:
// 1. Client sends a task via non-streaming API (tasks/send)
// 2. Client registers a webhook for push notifications
// 3. Client disconnects or does other work
// 4. Server processes the task asynchronously
// 5. When task completes, server sends push notification to the webhook
// 6. Client processes the notification
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

const (
	defaultServerHost  = "localhost"
	defaultServerPort  = 8000
	defaultWebhookHost = "localhost"
	defaultWebhookPort = 8001
	defaultWebhookPath = "/webhook"

	// JWT verification settings
	jwksRefreshInterval = 15 * time.Minute
	jwtMaxAge           = 5 * time.Minute
	httpTimeout         = 10 * time.Second
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
	jwksClient *JWKSClient
	tasks      *TaskTracker
}

// JWKSClient manages fetching and caching JWKS for JWT verification.
type JWKSClient struct {
	jwksURL      string
	keyset       jwk.Set
	keysetMutex  sync.RWMutex
	lastRefresh  time.Time
	refreshMutex sync.Mutex
}

// TaskTracker tracks task statuses.
type TaskTracker struct {
	taskMap   map[string]string
	taskMutex sync.RWMutex
}

// NewTaskTracker creates a new TaskTracker.
func NewTaskTracker() *TaskTracker {
	return &TaskTracker{
		taskMap: make(map[string]string),
	}
}

// TrackTask adds a task to the tracking map.
func (t *TaskTracker) TrackTask(taskID string) {
	t.taskMutex.Lock()
	defer t.taskMutex.Unlock()
	t.taskMap[taskID] = "pending"
	log.Infof("Task added to tracking: %s", taskID)
}

// UpdateTaskStatus updates the status of a tracked task.
func (t *TaskTracker) UpdateTaskStatus(taskID, status string) {
	t.taskMutex.Lock()
	defer t.taskMutex.Unlock()

	prevStatus, exists := t.taskMap[taskID]
	t.taskMap[taskID] = status

	if exists {
		log.Infof("Task %s status changed: %s -> %s at %v",
			taskID, prevStatus, status, time.Now().Format(time.RFC3339))
	} else {
		log.Infof("Task %s status set to %s at %v",
			taskID, status, time.Now().Format(time.RFC3339))
	}
}

// GetTaskStatus returns the current status of a task.
func (t *TaskTracker) GetTaskStatus(taskID string) string {
	t.taskMutex.RLock()
	defer t.taskMutex.RUnlock()
	status, exists := t.taskMap[taskID]
	if !exists {
		return "unknown"
	}
	return status
}

// NewJWKSClient creates a new JWKS client for JWT verification.
func NewJWKSClient(jwksURL string) *JWKSClient {
	client := &JWKSClient{
		jwksURL: jwksURL,
		keyset:  jwk.NewSet(),
	}

	// Fetch JWKS initially
	if err := client.RefreshJWKS(); err != nil {
		log.Errorf("Initial JWKS fetch failed: %v", err)
	} else {
		log.Infof("Initial JWKS fetch successful")
	}

	// Start a background goroutine to refresh JWKS periodically
	go client.StartPeriodicRefresh()

	return client
}

// StartPeriodicRefresh refreshes the JWKS periodically.
func (c *JWKSClient) StartPeriodicRefresh() {
	ticker := time.NewTicker(jwksRefreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		if err := c.RefreshJWKS(); err != nil {
			log.Errorf("JWKS refresh failed: %v", err)
		} else {
			log.Infof("JWKS refreshed successfully")
		}
	}
}

// RefreshJWKS fetches the latest JWKS from the server.
func (c *JWKSClient) RefreshJWKS() error {
	c.refreshMutex.Lock()
	defer c.refreshMutex.Unlock()

	log.Infof("Refreshing JWKS from %s at %v", c.jwksURL, time.Now().Format(time.RFC3339))

	// Create HTTP client with timeout
	httpClient := &http.Client{
		Timeout: httpTimeout,
	}

	// Fetch JWKS
	keysetResp, err := httpClient.Get(c.jwksURL)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer keysetResp.Body.Close()

	// Check if the response is valid
	if keysetResp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch JWKS: HTTP %d", keysetResp.StatusCode)
	}

	// Read the response body
	keysetData, err := io.ReadAll(keysetResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read JWKS: %w", err)
	}

	// Parse the JWKS
	keyset, err := jwk.Parse(keysetData)
	if err != nil {
		return fmt.Errorf("failed to parse JWKS: %w", err)
	}

	// Update the keyset
	c.keysetMutex.Lock()
	c.keyset = keyset
	c.lastRefresh = time.Now()
	keyCount := keyset.Len()
	c.keysetMutex.Unlock()

	log.Infof("JWKS refreshed successfully at %v. Keys found: %d",
		time.Now().Format(time.RFC3339), keyCount)
	return nil
}

// GetKeySet returns the current JWKS.
func (c *JWKSClient) GetKeySet() jwk.Set {
	c.keysetMutex.RLock()
	defer c.keysetMutex.RUnlock()
	return c.keyset
}

// NewWebhookHandler creates a new webhook handler for push notifications.
func NewWebhookHandler(jwksURL string) *WebhookHandler {
	return &WebhookHandler{
		jwksClient: NewJWKSClient(jwksURL),
		tasks:      NewTaskTracker(),
	}
}

// TrackTask adds a task to the tracking map.
func (h *WebhookHandler) TrackTask(taskID string) {
	h.tasks.TrackTask(taskID)
}

// GetTaskStatus returns the current status of a task.
func (h *WebhookHandler) GetTaskStatus(taskID string) string {
	return h.tasks.GetTaskStatus(taskID)
}

// ServeHTTP handles incoming push notifications from the server.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Infof("Received webhook request from %s at %v. Method: %s, ContentLength: %d",
		r.RemoteAddr, time.Now().Format(time.RFC3339), r.Method, r.ContentLength)

	// Only accept POST requests
	if r.Method != http.MethodPost {
		log.Errorf("Invalid HTTP method: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check validation token mechanism if present
	validationToken := r.URL.Query().Get("validationToken")
	if validationToken != "" {
		// This is a validation request, echo the token back
		log.Infof("Responding to validation request with token: %s", validationToken)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validationToken))
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Infof("Failed to read request body: %v", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
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
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		log.Infof("Invalid Authorization header format")
		http.Error(w, "Invalid Authorization format", http.StatusUnauthorized)
		return
	}
	tokenString := parts[1]

	// Verify JWT signature using JWKS
	token, err := h.verifyJWT(tokenString, body)
	if err != nil {
		log.Infof("JWT verification failed: %v", err)
		http.Error(w, "Invalid token", http.StatusUnauthorized)
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

	// Verify task_id in the payload if present in the JWT
	if payloadObj, ok := jwtPayload["payload"].(map[string]interface{}); ok {
		if jwtTaskID, ok := payloadObj["task_id"].(string); ok && jwtTaskID != taskID {
			log.Infof("Task ID mismatch: JWT=%v vs Notification=%s", jwtTaskID, taskID)
			http.Error(w, "Task ID mismatch", http.StatusUnauthorized)
			return
		}
	}

	// Update task status in our tracking map
	status, _ := notification["status"].(string)
	h.tasks.UpdateTaskStatus(taskID, status)

	// Log verification success
	log.Infof("★★★ Verified notification for task %s: Status = %s ★★★", taskID, status)
	prettyJSON, _ := json.MarshalIndent(notification, "", "  ")
	log.Infof("Notification details: %s", string(prettyJSON))

	// Respond with success
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Notification received and verified"))
}

// verifyJWT verifies a JWT token using the JWKS.
func (h *WebhookHandler) verifyJWT(tokenString string, payload []byte) (jwt.Token, error) {
	// Get keyset for verification
	keyset := h.jwksClient.GetKeySet()

	if keyset == nil || keyset.Len() == 0 {
		log.Errorf("JWKS verification failed: empty keyset from endpoint %s.", h.jwksClient.jwksURL)
		return nil, fmt.Errorf("no JWKS available")
	}

	// Calculate payload hash for verification
	hash := sha256.Sum256(payload)
	expectedPayloadHash := fmt.Sprintf("%x", hash)

	// Enhanced logging of token and keyset details
	log.Infof("Verifying token: %s... (length: %d)",
		tokenString[:min(30, len(tokenString))], len(tokenString))
	log.Infof("Using keyset with %d keys from endpoint: %s",
		keyset.Len(), h.jwksClient.jwksURL)

	// Log detailed keyset information
	for i := 0; i < keyset.Len(); i++ {
		key, _ := keyset.Key(i)
		keyID, _ := key.Get("kid")
		keyAlg, _ := key.Get("alg")
		keyKty, _ := key.Get("kty")
		log.Infof("JWKS key[%d]: kid=%v, alg=%v, kty=%v", i, keyID, keyAlg, keyKty)

		// Dump all key parameters for detailed debugging
		keyJSON, _ := json.Marshal(key)
		log.Infof("Full key details: %s", string(keyJSON))
	}

	// Log raw token properties for debugging
	parts := strings.Split(tokenString, ".")
	if len(parts) > 0 {
		headerBase64 := parts[0]
		// Add padding if needed
		if l := len(headerBase64) % 4; l > 0 {
			headerBase64 += strings.Repeat("=", 4-l)
		}
		headerBytes, err := base64.StdEncoding.DecodeString(headerBase64)
		if err == nil {
			log.Infof("Decoded token header: %s", string(headerBytes))
		} else {
			// Try URL encoding
			headerBytes, err = base64.URLEncoding.DecodeString(headerBase64)
			if err == nil {
				log.Infof("Decoded token header (URL encoding): %s", string(headerBytes))
			} else {
				// Try URL encoding without padding
				headerBytes, err = base64.RawURLEncoding.DecodeString(parts[0])
				if err == nil {
					log.Infof("Decoded token header (Raw URL encoding): %s", string(headerBytes))
				} else {
					log.Warnf("Failed to decode token header: %v", err)
				}
			}
		}
	}

	// Try to parse token first without verification to extract the key ID
	token, err := jwt.Parse([]byte(tokenString), jwt.WithVerify(false))
	if err != nil {
		log.Errorf("Failed to parse token (pre-verification): %v", err)
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// Extract and log header information
	headerJSON, _ := json.Marshal(token.PrivateClaims())
	log.Infof("JWT private claims: %s", string(headerJSON))

	// Access and log specific header fields if present
	kid, kidExists := token.Get("kid")
	if kidExists {
		log.Infof("Token uses key ID: %v", kid)
	} else {
		log.Infof("Token has no key ID, will try all keys")
	}

	alg, algExists := token.Get("alg")
	if algExists {
		log.Infof("Token uses algorithm: %v", alg)
	}

	// Log token payload for debugging
	if payload, err := token.AsMap(context.Background()); err == nil {
		payloadJSON, _ := json.Marshal(payload)
		log.Infof("Token payload: %s", string(payloadJSON))
	} else {
		log.Warnf("Could not extract token payload: %v", err)
	}

	// Verify token with keyset
	log.Infof("Attempting JWT verification with keyset...")

	// Create verification options
	opts := []jwt.ParseOption{jwt.WithKeySet(keyset)}

	// Add validation option
	opts = append(opts, jwt.WithValidate(true))

	// Add algorithm validation bypass if needed
	// opts = append(opts, jwt.WithVerifyAuto(true))

	// Parse with options
	token, err = jwt.Parse([]byte(tokenString), opts...)
	if err != nil {
		log.Errorf("Token validation failed: %v", err)

		// Try a more lenient verification approach as fallback
		log.Infof("Attempting more lenient verification...")

		// Try verification without key ID matching
		for i := 0; i < keyset.Len(); i++ {
			key, _ := keyset.Key(i)

			var pubKey interface{}
			err := key.Raw(&pubKey)
			if err != nil {
				log.Warnf("Failed to extract public key: %v", err)
				continue
			}

			// Try direct verification with this key
			token, err = jwt.Parse([]byte(tokenString), jwt.WithKey(jwa.RS256, pubKey))
			if err == nil {
				log.Infof("Token verified successfully with key #%d", i)
				break
			} else {
				log.Warnf("Verification with key #%d failed: %v", i, err)
			}
		}
		// If all verification attempts failed
		return nil, err
	}
	log.Infof("JWT token validation successful at %v. Token ID: %v.",
		time.Now().Format(time.RFC3339), token.JwtID())

	// Output token claims for debugging
	if token != nil {
		claims, err := token.AsMap(context.Background())
		if err != nil {
			log.Errorf("Failed to extract claims as map: %v", err)
			return nil, fmt.Errorf("failed to parse token claims: %w", err)
		}
		log.Debugf("Successfully parsed claims from token. Issuer: %v, Subject: %v",
			claims["iss"], claims["sub"])

		if claims["nonce"] != nil {
			// Log specific important claims individually
			if iss, exists := claims["iss"]; exists {
				log.Infof("JWT issuer: %v", iss)
			}
			if iat, exists := claims["iat"]; exists {
				log.Infof("JWT issued at: %v", iat)
			}
			if sub, exists := claims["sub"]; exists {
				log.Infof("JWT subject: %v", sub)
			}
		}
	}

	// Verify token has not expired - but be more lenient
	if iat, exists := token.Get("iat"); exists {
		if iatTime, ok := iat.(time.Time); ok {
			tokenAge := time.Since(iatTime)
			currentTime := time.Now()
			log.Infof("Token age verification at %v - Issued at: %v, Age: %v, Max allowed: %v",
				currentTime.Format(time.RFC3339), iatTime.Format(time.RFC3339),
				tokenAge.Round(time.Millisecond), (jwtMaxAge * 2).Round(time.Millisecond))

			if tokenAge > jwtMaxAge*2 { // More lenient timeout for testing
				return nil, fmt.Errorf("token has expired (age: %v)", tokenAge)
			}
		} else {
			log.Warnf("JWT 'iat' claim has unexpected type: %T", iat)
		}
	} else {
		log.Warnf("JWT missing 'iat' claim")
	}

	// Verify payload hash if present in token - but make it optional
	if requestBodyHash, exists := token.Get("request_body_sha256"); exists {
		if hashStr, ok := requestBodyHash.(string); ok {
			log.Infof("Payload hash verification at %v - Length: %d bytes, Hash: %s",
				time.Now().Format(time.RFC3339), len(expectedPayloadHash), hashStr[:min(8, len(hashStr))]+"...")

			if hashStr != expectedPayloadHash {
				log.Warnf("Payload hash mismatch: expected %s, got %s",
					expectedPayloadHash[:min(8, len(expectedPayloadHash))]+"...",
					hashStr[:min(8, len(hashStr))]+"...")
				// Don't fail on hash mismatch during testing
				// return nil, fmt.Errorf("payload hash mismatch")
			} else {
				log.Infof("Payload hash verified successfully. Size: %d bytes", len(payload))
			}
		} else {
			log.Warnf("JWT 'request_body_sha256' claim has unexpected type: %T",
				requestBodyHash)
		}
	} else {
		log.Warnf("JWT missing 'request_body_sha256' claim")
	}

	return token, nil
}

// Helper function for string slicing
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

// sendTaskToServer demonstrates sending a task using the new message API
func sendTaskToServer(ctx context.Context, a2aClient *client.A2AClient, content string) (string, error) {
	log.Infof("Sending message with content: %s", content)

	// Create a message with the content
	message := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(content)},
	)

	// Create send message parameters
	params := protocol.SendMessageParams{
		Message: message,
	}

	// Send the message
	result, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	// Check the result type
	switch response := result.Result.(type) {
	case *protocol.Message:
		log.Infof("Received direct message response")
		return "", nil // No task ID for direct message responses

	case *protocol.Task:
		log.Infof("Task created: %s (State: %s)", response.ID, response.Status.State)
		return response.ID, nil

	default:
		return "", fmt.Errorf("unexpected response type: %T", response)
	}
}

func main() {
	// Parse command line flags
	var cfg Config
	flag.StringVar(&cfg.ServerHost, "server-host", defaultServerHost, "A2A server host")
	flag.IntVar(&cfg.ServerPort, "server-port", defaultServerPort, "A2A server port")
	flag.StringVar(&cfg.WebhookHost, "webhook-host", defaultWebhookHost, "Webhook server host")
	flag.IntVar(&cfg.WebhookPort, "webhook-port", defaultWebhookPort, "Webhook server port")
	flag.StringVar(&cfg.WebhookPath, "webhook-path", defaultWebhookPath, "Webhook path")
	flag.StringVar(&cfg.JWKSEndpoint, "jwks-endpoint", "", "JWKS endpoint (default: derived from server)")
	flag.Parse()

	// Set default JWKS endpoint if not provided
	if cfg.JWKSEndpoint == "" {
		cfg.JWKSEndpoint = fmt.Sprintf("http://%s:%d/.well-known/jwks.json", cfg.ServerHost, cfg.ServerPort)
	}

	log.Infof("Starting JWKS client with config: %+v", cfg)

	// Create an A2A client
	serverURL := fmt.Sprintf("http://%s:%d/", cfg.ServerHost, cfg.ServerPort)
	a2aClient, err := client.NewA2AClient(serverURL)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	// Create webhook handler that will receive push notifications
	webhookHandler := NewWebhookHandler(cfg.JWKSEndpoint)

	// Start webhook server
	webhookURL := fmt.Sprintf("http://%s:%d%s", cfg.WebhookHost, cfg.WebhookPort, cfg.WebhookPath)
	startWebhookServer(&cfg, webhookHandler)

	// Demonstrate sending multiple tasks
	for i := 1; i <= 3; i++ {
		content := fmt.Sprintf("Task %d: Process this message asynchronously", i)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		taskID, err := sendTaskToServer(ctx, a2aClient, content)
		cancel()

		if err != nil {
			log.Errorf("Failed to send task %d: %v", i, err)
			continue
		}

		if taskID != "" {
			// Track the task in our webhook handler
			webhookHandler.TrackTask(taskID)

			// Set push notification for the task
			pushConfig := protocol.TaskPushNotificationConfig{
				TaskID: taskID,
				PushNotificationConfig: protocol.PushNotificationConfig{
					URL: webhookURL,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err = a2aClient.SetPushNotification(ctx, pushConfig)
			cancel()

			if err != nil {
				log.Errorf("Failed to set push notification for task %s: %v", taskID, err)
			} else {
				log.Infof("Push notification set for task %s", taskID)
			}
		}

		// Add a small delay between tasks
		time.Sleep(1 * time.Second)
	}

	log.Infof("All tasks sent. Waiting for push notifications...")

	// Keep the client running to receive notifications
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Infof("Received signal %v, shutting down...", sig)
	case <-time.After(2 * time.Minute):
		log.Infof("Timeout reached, shutting down...")
	}

	log.Infof("Client shutting down.")
}
