// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a client that registers a webhook and verifies
// signed push notifications with the SDK's JWKS support.
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
	"sync"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
)

const (
	defaultServerHost  = "localhost"
	defaultServerPort  = 8000
	defaultWebhookHost = "localhost"
	defaultWebhookPort = 8001
	defaultWebhookPath = "/webhook"
)

type config struct {
	serverHost   string
	serverPort   int
	webhookHost  string
	webhookPort  int
	webhookPath  string
	jwksEndpoint string
}

type taskTracker struct {
	mu       sync.Mutex
	statuses map[string]string
}

func newTaskTracker() *taskTracker {
	return &taskTracker{statuses: make(map[string]string)}
}

func (t *taskTracker) track(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.statuses[taskID] = "pending"
	log.Infof("Task added to tracking: %s", taskID)
}

func (t *taskTracker) update(taskID, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := t.statuses[taskID]
	t.statuses[taskID] = status
	log.Infof("Task %s status changed: %s -> %s", taskID, previous, status)
}

type webhookHandler struct {
	verifier *pushauth.Authenticator
	tasks    *taskTracker
}

func newWebhookHandler(jwksURL string) *webhookHandler {
	verifier := pushauth.NewAuthenticator()
	// SetJWKSClient configures the SDK's cached JWKSClient. Verification fetches
	// keys on demand and reuses them until the cache expires.
	verifier.SetJWKSClient(jwksURL)
	return &webhookHandler{verifier: verifier, tasks: newTaskTracker()}
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}
	if err := h.verifier.VerifyPushNotification(r, body); err != nil {
		log.Errorf("Push notification verification failed: %v", err)
		http.Error(w, "invalid push notification", http.StatusUnauthorized)
		return
	}

	var notification protocol.StreamResponse
	if err := json.Unmarshal(body, &notification); err != nil {
		http.Error(w, "invalid push notification body", http.StatusBadRequest)
		return
	}

	var taskID, status string
	if update := notification.GetStatusUpdate(); update != nil {
		taskID = update.TaskID
		status = string(update.Status.State)
	} else if task := notification.GetTask(); task != nil {
		taskID = task.ID
		status = string(task.Status.State)
	}
	if taskID == "" {
		http.Error(w, "push notification has no task ID", http.StatusBadRequest)
		return
	}

	h.tasks.update(taskID, status)
	log.Infof("Verified push notification for task %s: %s", taskID, status)
	w.WriteHeader(http.StatusNoContent)
}

func startWebhookServer(cfg *config, handler http.Handler) {
	addr := fmt.Sprintf("%s:%d", cfg.webhookHost, cfg.webhookPort)
	mux := http.NewServeMux()
	mux.Handle(cfg.webhookPath, handler)
	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Infof("Webhook listening at http://%s%s", addr, cfg.webhookPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Webhook server failed: %v", err)
		}
	}()
}

func sendMessage(
	ctx context.Context,
	a2aClient *client.A2AClient,
	content string,
) (string, error) {
	message := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(content)},
	)
	returnImmediately := true
	result, err := a2aClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: message,
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnImmediately,
		},
	})
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	if task := result.GetTask(); task != nil {
		log.Infof("Task created: %s (%s)", task.ID, task.Status.State)
		return task.ID, nil
	}
	if result.GetMessage() != nil {
		return "", nil
	}
	return "", fmt.Errorf("unexpected empty SendMessage response")
}

func main() {
	var cfg config
	flag.StringVar(&cfg.serverHost, "server-host", defaultServerHost, "A2A server host")
	flag.IntVar(&cfg.serverPort, "server-port", defaultServerPort, "A2A server port")
	flag.StringVar(&cfg.webhookHost, "webhook-host", defaultWebhookHost, "Webhook server host")
	flag.IntVar(&cfg.webhookPort, "webhook-port", defaultWebhookPort, "Webhook server port")
	flag.StringVar(&cfg.webhookPath, "webhook-path", defaultWebhookPath, "Webhook path")
	flag.StringVar(&cfg.jwksEndpoint, "jwks-endpoint", "", "JWKS endpoint (default: derived from server)")
	flag.Parse()

	serverURL := fmt.Sprintf("http://%s:%d/", cfg.serverHost, cfg.serverPort)
	if cfg.jwksEndpoint == "" {
		cfg.jwksEndpoint = serverURL + ".well-known/jwks.json"
	}
	a2aClient, err := client.NewA2AClient(serverURL)
	if err != nil {
		log.Fatalf("Create A2A client: %v", err)
	}

	webhook := newWebhookHandler(cfg.jwksEndpoint)
	webhookURL := fmt.Sprintf(
		"http://%s:%d%s", cfg.webhookHost, cfg.webhookPort, cfg.webhookPath,
	)
	startWebhookServer(&cfg, webhook)

	for i := 1; i <= 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		taskID, err := sendMessage(
			ctx, a2aClient,
			fmt.Sprintf("Task %d: process this message asynchronously", i),
		)
		cancel()
		if err != nil {
			log.Errorf("Send task %d: %v", i, err)
			continue
		}
		if taskID == "" {
			continue
		}

		webhook.tasks.track(taskID)
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		_, err = a2aClient.SetPushNotification(ctx, protocol.TaskPushNotificationConfig{
			TaskID: taskID,
			URL:    webhookURL,
		})
		cancel()
		if err != nil {
			log.Errorf("Register webhook for task %s: %v", taskID, err)
		} else {
			log.Infof("Webhook registered for task %s", taskID)
		}
		time.Sleep(time.Second)
	}

	log.Infof("Tasks submitted; waiting for push notifications")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Minute):
	}
}
