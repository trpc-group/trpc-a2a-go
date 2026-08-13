// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates API-key and JWT authentication with the A2A client.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

const (
	defaultJWTSecret   = "auth-example-shared-secret"
	defaultJWTAudience = "a2a-server"
	defaultJWTIssuer   = "auth-example"
)

var (
	url       = flag.String("url", "http://localhost:8080/", "A2A server URL")
	method    = flag.String("auth", "apikey", "Authentication method: apikey or jwt")
	apiKey    = flag.String("api-key", "alice-key", "API key (alice-key or bob-key)")
	jwtSecret = flag.String("jwt-secret", defaultJWTSecret, "Shared JWT signing secret")
	message   = flag.String("message", "Hello, authenticated world", "Message to send")
)

func main() {
	flag.Parse()

	a2aClient, err := newClient(*url, *method, *apiKey, *jwtSecret)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := a2aClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart(*message)},
		),
	})
	if err != nil {
		log.Fatalf("SendMessage failed: %v", err)
	}

	task := result.GetTask()
	if task == nil {
		log.Fatalf("Expected a task, got %T", result)
	}
	fmt.Printf("Task ID: %s\n", task.ID)
	fmt.Printf("Status: %s\n", task.Status.State)
	if task.Status.Message != nil {
		fmt.Printf("Response: %s\n", extractText(*task.Status.Message))
	}

	// The owner that created the task can retrieve it.
	stored, err := a2aClient.GetTasks(ctx, protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		log.Fatalf("GetTask failed: %v", err)
	}
	fmt.Printf("Same-owner GetTask succeeded; status=%s\n", stored.Status.State)

	// API-key mode also proves the negative path with the other demo owner.
	if *method == "apikey" {
		otherAPIKey := "bob-key"
		if *apiKey == otherAPIKey {
			otherAPIKey = "alice-key"
		}
		otherClient, err := newClient(*url, *method, otherAPIKey, *jwtSecret)
		if err != nil {
			log.Fatalf("Create cross-owner client failed: %v", err)
		}
		if _, err := otherClient.GetTasks(ctx, protocol.TaskQueryParams{ID: task.ID}); err == nil || !strings.Contains(
			err.Error(), `"code":-32001`,
		) {
			log.Fatalf("Cross-owner GetTask error = %v, want task-not-found", err)
		}
		fmt.Println("Cross-owner GetTask denied with task-not-found")
	}
}

func newClient(url, method, apiKey, jwtSecret string) (*client.A2AClient, error) {
	switch method {
	case "apikey":
		return client.NewA2AClient(url, client.WithAPIKeyAuth(apiKey, "X-API-Key"))
	case "jwt":
		return client.NewA2AClient(url, client.WithJWTAuth(
			[]byte(jwtSecret),
			defaultJWTAudience,
			defaultJWTIssuer,
			time.Hour,
		))
	default:
		return nil, fmt.Errorf("unsupported auth method %q: use apikey or jwt", method)
	}
}

func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}
