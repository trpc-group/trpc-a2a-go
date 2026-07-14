// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command client runs a webhook receiver, registers it with the automatic-push
// agent, and waits for the terminal notification.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
)

const (
	defaultAgentURL      = "http://localhost:8000"
	defaultWebhookListen = ":8001"
	defaultWebhookURL    = "http://localhost:8001/notify"
)

func main() {
	agentURL := flag.String("agent-url", defaultAgentURL, "A2A agent base URL")
	webhookListen := flag.String("webhook-listen", defaultWebhookListen, "webhook listen address")
	webhookURL := flag.String("webhook-url", defaultWebhookURL, "webhook URL registered with the agent")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	delivered := make(chan string, 8)
	verifier := pushauth.NewAuthenticator()
	verifier.SetJWKSClient(strings.TrimRight(*agentURL, "/") + protocol.JWKSPath)
	webhook := &http.Server{Handler: webhookHandler(verifier, delivered)}
	listener, err := net.Listen("tcp", *webhookListen)
	if err != nil {
		log.Fatalf("listen for webhook: %v", err)
	}
	go func() {
		if err := webhook.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve webhook: %v", err)
		}
	}()
	defer webhook.Close()
	log.Printf("webhook listening at %s", *webhookURL)

	log.Print("sending message and registering webhook...")
	sendMessage(*agentURL, *webhookURL)
	log.Print("request returned; waiting for automatic push instead of polling")

	select {
	case state := <-delivered:
		log.Printf("received verified notification: task state = %s", state)
	case <-time.After(3 * time.Second):
		log.Fatal("timed out waiting for a push notification")
	}
}

func webhookHandler(verifier *pushauth.Authenticator, delivered chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if err := verifier.VerifyPushNotification(r, body); err != nil {
			log.Printf("webhook rejected notification: %v", err)
			http.Error(w, "verification failed", http.StatusUnauthorized)
			return
		}
		var resp protocol.StreamResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			http.Error(w, "decode notification", http.StatusBadRequest)
			return
		}
		state := protocol.TaskStateUnspecified
		if update := resp.GetStatusUpdate(); update != nil {
			state = update.Status.State
		}
		w.WriteHeader(http.StatusOK)
		delivered <- string(state)
	})
}

func sendMessage(agentURL, webhookURL string) {
	c, err := client.NewA2AClient(agentURL)
	if err != nil {
		log.Fatalf("create client: %v", err)
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
		log.Fatalf("send message: %v", err)
	}
}
