// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command manual demonstrates MANUAL push notifications.
//
// The TaskManager is built with push.Config{ManualDelivery: true}: the
// framework's automatic dispatch is disabled, but push stays fully supported —
// clients can register webhooks, the capability is advertised, and the JWKS is
// still published. The agent delivers on its own schedule, from inside the
// processor, by calling the Notifier itself. Here it pushes a mid-task
// milestone AND the final result — the timing and content are entirely the
// agent's choice.
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
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Signing Notifier — the agent calls it directly to deliver.
	notifier, err := pushauth.NewNotifier(pushauth.WithJWT())
	if err != nil {
		log.Fatalf("notifier: %v", err)
	}
	log.Print("[setup]   built a signing notifier — the agent will call it directly")

	// The agent server still publishes JWKS (identity passed in startAgent);
	// only automatic delivery is off (push.Config{ManualDelivery: true}).
	agent, tm := startAgent(notifier)
	defer tm.Close()
	defer agent.Close()
	log.Printf("[agent]   listening at %s", agent.URL)
	log.Printf("[agent]   JWKS auto-published at %s (auto-delivery OFF — manual mode)", agent.URL+protocol.JWKSPath)

	// A webhook receiver that verifies every notification against the agent's JWKS.
	delivered := make(chan string, 8)
	webhook := startWebhook(delivered, agent.URL+protocol.JWKSPath)
	defer webhook.Close()
	log.Printf("[webhook] listening at %s — will verify pushes against the agent JWKS", webhook.URL)

	// The client registers its webhook and returns immediately; the agent pushes
	// on its own schedule.
	log.Print("[client]  sending message, registering webhook, returning immediately...")
	sendMessage(agent.URL, webhook.URL)
	log.Print("[client]  returned; the agent will push on its own schedule")

	awaitPushes(delivered, 2)
	log.Print("[done]    received both agent-initiated notifications")
}

// worker delivers push notifications itself, at moments it chooses. The
// framework does not auto-deliver (the manager runs in manual delivery mode).
type worker struct {
	notifier *pushauth.Notifier
}

func (p *worker) ProcessMessage(
	ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	h := taskmanager.NewTaskHandle(ctx, ec)
	// The inline webhook from the send request. Note: configs registered later
	// via tasks/pushNotificationConfig/set are NOT visible here — an agent that
	// must honor those needs the TaskManager (OnPushNotificationList) as well.
	cfg := ec.PushConfig
	log.Printf("[agent]   task %s received; the agent controls delivery", ec.TaskID)
	go func() {
		defer h.Close()
		h.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText("working..."))
		log.Printf("[agent]   task %s -> working", ec.TaskID)

		// Agent-chosen milestone: the framework would never send this on its own.
		time.Sleep(300 * time.Millisecond)
		log.Print("[agent]   milestone reached; pushing an update myself (framework would not)")
		p.push(ctx, cfg, ec.TaskID, protocol.TaskStateWorking, "milestone: halfway done")

		// Final result, delivered when the agent decides it is meaningful.
		time.Sleep(300 * time.Millisecond)
		h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
		log.Printf("[agent]   task %s -> completed; pushing the final result myself", ec.TaskID)
		p.push(ctx, cfg, ec.TaskID, protocol.TaskStateCompleted, "all done")
	}()
	return h.Events(), nil
}

// push delivers one notification to the client's webhook via the Notifier.
func (p *worker) push(ctx context.Context, cfg *protocol.TaskPushNotificationConfig,
	taskID string, state protocol.TaskState, note string) {
	if cfg == nil {
		log.Print("[agent]   no webhook registered; nothing to push")
		return
	}
	ev := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
		Status: protocol.TaskStatus{State: state, Message: protocol.NewAgentText(note)},
	})
	if err := p.notifier.SendPush(ctx, *cfg, ev); err != nil {
		log.Printf("[agent]   manual push failed: %v", err)
	}
}

// startAgent builds a TaskManager with push enabled but delivery manual —
// registration and JWKS on, automatic dispatch off — and serves it over HTTP.
func startAgent(notifier *pushauth.Notifier) (*httptest.Server, *memory.TaskManager) {
	proc := &worker{notifier: notifier}
	tm, err := memory.NewTaskManager(proc, memory.WithPushNotificationsConfig(push.Config{
		Sender:         notifier,
		ManualDelivery: true,
	}))
	if err != nil {
		log.Fatalf("task manager: %v", err)
	}
	card := server.AgentCard{Name: "manual-notify", URL: "http://localhost/", Version: "1.0.0"}
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
// delivered note on ch.
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
		note := ""
		if su := resp.GetStatusUpdate(); su != nil && su.Status.Message != nil &&
			len(su.Status.Message.Parts) > 0 {
			note = su.Status.Message.Parts[0].TextContent()
		}
		log.Printf("[webhook] signature OK — %q", note)
		w.WriteHeader(http.StatusOK)
		ch <- note
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
