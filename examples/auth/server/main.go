// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates built-in API-key and JWT authentication together
// with owner-scoped task storage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/auth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const (
	defaultJWTSecret   = "auth-example-shared-secret"
	defaultJWTAudience = "a2a-server"
	defaultJWTIssuer   = "auth-example"
)

var (
	apiKeys = map[string]string{
		"alice-key": "alice",
		"bob-key":   "bob",
	}
	host      = flag.String("host", "localhost", "Host address to bind to")
	port      = flag.Int("port", 8080, "Port to listen on")
	jwtSecret = flag.String("jwt-secret", defaultJWTSecret, "Shared JWT signing secret")
)

func main() {
	flag.Parse()

	baseURL := fmt.Sprintf("http://%s:%d/", *host, *port)

	// The chain accepts either an API key or a JWT. Both providers return an
	// auth.User whose ID becomes the task owner below.
	provider := auth.NewChainAuthProvider(
		auth.NewAPIKeyAuthProvider(apiKeys, "X-API-Key"),
		auth.NewJWTAuthProvider(
			[]byte(*jwtSecret),
			defaultJWTAudience,
			defaultJWTIssuer,
			time.Hour,
		),
	)

	taskManager, err := memory.NewTaskManager(
		&echoMessageProcessor{},
		memory.WithOwnerResolver(resolveOwner),
	)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	srv, err := server.NewA2AServer(
		taskManager,
		server.WithAgentCard(buildAgentCard(baseURL)),
		server.WithAuthProvider(provider),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	go func() {
		addr := fmt.Sprintf("%s:%d", *host, *port)
		log.Printf("Starting server on %s", addr)
		log.Printf("API keys: alice-key -> alice, bob-key -> bob")
		log.Printf("JWT: issuer=%s audience=%s", defaultJWTIssuer, defaultJWTAudience)
		if err := srv.Start(addr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("Received signal %v, shutting down", <-sigCh)
}

func resolveOwner(ctx context.Context) (string, error) {
	user, ok := auth.UserFromContext(ctx)
	if !ok || user.ID == "" {
		return "", errors.New("authenticated user is missing")
	}
	return user.ID, nil
}

func buildAgentCard(baseURL string) server.AgentCard {
	return server.AgentCard{
		Name:        "A2A Authentication Example",
		Description: "API-key and JWT authentication with per-user task isolation",
		URL:         baseURL,
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:         boolPtr(false),
			PushNotifications: boolPtr(false),
		},
		SecuritySchemes: map[string]server.SecurityScheme{
			"apiKey": {
				Type:        "apiKey",
				Description: stringPtr("API key authentication"),
				Name:        stringPtr("X-API-Key"),
				In:          securitySchemeInPtr(server.SecuritySchemeInHeader),
			},
			"jwt": {
				Type:         "http",
				Description:  stringPtr("JWT Bearer authentication"),
				Scheme:       stringPtr("bearer"),
				BearerFormat: stringPtr("JWT"),
			},
		},
		SecurityRequirements: []map[string][]string{
			{"apiKey": {}},
			{"jwt": {}},
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "echo",
				Name:        "Authenticated Echo",
				Description: stringPtr("Echoes text and returns the authenticated owner"),
				Tags:        []string{"text", "echo", "auth"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}
}

type echoMessageProcessor struct{}

func (p *echoMessageProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	user, _ := auth.UserFromContext(ctx)
	text := extractText(ec.Message)
	response := protocol.NewAgentText(fmt.Sprintf("owner=%s echo=%q", user.ID, text))
	if err := handle.UpdateTaskState(protocol.TaskStateCompleted, response); err != nil {
		return nil, err
	}
	return handle.Events(), nil
}

func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

func boolPtr(v bool) *bool {
	return &v
}

func stringPtr(v string) *string {
	return &v
}

func securitySchemeInPtr(v server.SecuritySchemeIn) *server.SecuritySchemeIn {
	return &v
}
