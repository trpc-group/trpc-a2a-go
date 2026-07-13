// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command auto demonstrates AUTOMATIC push notifications.
//
// A signing pushauth.Notifier is wired into the TaskManager (its Sender), and its
// identity is passed to the server (WithPushNotificationAuthenticator) so the
// server publishes the matching JWKS. The framework then delivers a
// StreamResponse to every registered webhook as the task reaches a significant
// state. The message processor never sends a notification itself.
//
// The log is tagged by actor ([agent] / [webhook] / [client]) and timestamped,
// so the interleaved output reads as one timeline.
//
// Run it: go run .  (self-contained: server, webhook, and client in one process)
package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// One signing Notifier carries the whole push capability (JWT identity + delivery).
	notifier, err := pushauth.NewNotifier(pushauth.WithJWT())
	if err != nil {
		log.Fatalf("notifier: %v", err)
	}
	log.Print("[setup]   built a signing notifier — its JWT identity is the whole push capability")

	// The agent server publishes the notifier's JWKS (identity passed in startAgent).
	agent, tm := startAgent(notifier)
	defer tm.Close()
	defer agent.Close()
	log.Printf("[agent]   listening at %s", agent.URL)
	log.Printf("[agent]   JWKS auto-published at %s", agent.URL+protocol.JWKSPath)

	// A webhook receiver that verifies every notification against the agent's JWKS.
	delivered := make(chan string, 8)
	webhook := startWebhook(delivered, agent.URL+protocol.JWKSPath)
	defer webhook.Close()
	log.Printf("[webhook] listening at %s — will verify pushes against the agent JWKS", webhook.URL)

	// The client registers its webhook and returns immediately; it learns the
	// outcome through the webhook, not the response.
	log.Print("[client]  sending message, registering webhook, returning immediately...")
	sendMessage(agent.URL, webhook.URL)
	log.Print("[client]  returned; NOT waiting for the result — it will arrive via the webhook")

	awaitPushes(delivered, 1)
	log.Print("[done]    got the notification without ever polling the agent")
}

// worker completes the task asynchronously and reports state. It does NOT send
// push notifications — the framework does that automatically.
type worker struct{}

func (worker) ProcessMessage(
	ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	h := taskmanager.NewTaskHandle(ctx, ec)
	log.Printf("[agent]   task %s received; processing asynchronously", ec.TaskID)
	go func() {
		defer h.Close()
		// A content-less working heartbeat is not pushed (only significant
		// state changes are), so the webhook receives exactly the terminal event.
		h.UpdateTaskState(protocol.TaskStateWorking, nil)
		log.Printf("[agent]   task %s -> working (heartbeat, not pushed)", ec.TaskID)
		time.Sleep(300 * time.Millisecond)
		h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
		log.Printf("[agent]   task %s -> completed; framework will auto-push to registered webhooks", ec.TaskID)
	}()
	return h.Events(), nil
}

// startAgent builds a TaskManager whose push capability is the notifier and
// serves it over HTTP, publishing the notifier's JWKS.
func startAgent(notifier *pushauth.Notifier) (*httptest.Server, *memory.TaskManager) {
	tm, err := memory.NewTaskManager(worker{}, memory.WithPushNotifications(notifier))
	if err != nil {
		log.Fatalf("task manager: %v", err)
	}
	card := server.AgentCard{Name: "auto-notify", URL: "http://localhost/", Version: "1.0.0"}
	// The signing identity is configured explicitly so the server publishes the
	// matching JWKS; notifier.Authenticator() is the same identity it signs with.
	srv, err := server.NewA2AServer(tm,
		server.WithAgentCard(card),
		server.WithPushNotificationAuthenticator(notifier.Authenticator()),
	)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	return httptest.NewServer(srv.Handler()), tm
}

// startWebhook runs the client's webhook: it builds a verifier bound to the
// agent's JWKS endpoint, checks every push against it, and reports the
// delivered task state on ch.
func startWebhook(ch chan<- string, jwksURL string) *httptest.Server {
	verifier := pushauth.NewAuthenticator()
	verifier.SetJWKSClient(jwksURL)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.Printf("[webhook] received a POST (%d bytes); verifying JWT signature via JWKS...", len(body))
		if err := verifier.VerifyPushNotification(r, body); err != nil {
			log.Printf("[webhook] REJECTED: %v", err)
			http.Error(w, "verification failed", http.StatusUnauthorized)
			ch <- "rejected"
			return
		}
		var resp protocol.StreamResponse
		_ = json.Unmarshal(body, &resp)
		state := "?"
		if su := resp.GetStatusUpdate(); su != nil {
			state = string(su.Status.State)
		}
		log.Printf("[webhook] signature OK — task state = %s", state)
		w.WriteHeader(http.StatusOK)
		ch <- state
	}))
}

// sendMessage asks the agent to do work, registering webhookURL for push updates
// and returning immediately instead of waiting for the result.
func sendMessage(agentURL, webhookURL string) {
	c, err := client.NewA2AClient(agentURL)
	if err != nil {
		log.Fatalf("client: %v", err)
	}
	returnNow := true
	_, err = c.SendMessage(context.Background(), protocol.SendMessageParams{
		Message: protocol.Message{
			Role:  protocol.MessageRoleUser,
			Parts: []*protocol.Part{protocol.NewTextPart("please do the work")},
		},
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately: &returnNow,
			PushConfig:        &protocol.TaskPushNotificationConfig{URL: webhookURL},
		},
	})
	if err != nil {
		log.Fatalf("send: %v", err)
	}
}

// awaitPushes blocks until n notifications have been reported by the webhook.
func awaitPushes(ch <-chan string, n int) {
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			log.Fatal("timed out waiting for a push notification")
		}
	}
}
