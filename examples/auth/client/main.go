// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command client calls the authenticated A2A example with JWT or API-key authentication.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

type config struct {
	authMethod    string
	agentURL      string
	timeout       time.Duration
	message       string
	contextID     string
	jwtSecretFile string
	jwtAudience   string
	jwtIssuer     string
	jwtExpiry     time.Duration
	apiKey        string
	apiKeyHeader  string
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.authMethod, "auth", "jwt", "authentication method: jwt or apikey")
	flag.StringVar(&cfg.agentURL, "url", "http://localhost:8080", "target A2A endpoint")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Second, "timeout for each example request")
	flag.StringVar(&cfg.message, "message", "Hello from an authenticated client", "message to send")
	flag.StringVar(&cfg.contextID, "context-id", "", "optional conversation context ID")
	flag.StringVar(&cfg.jwtSecretFile, "jwt-secret-file", "jwt-secret.key", "shared JWT secret file for this local demo")
	flag.StringVar(&cfg.jwtAudience, "jwt-audience", "a2a-server", "JWT audience")
	flag.StringVar(&cfg.jwtIssuer, "jwt-issuer", "auth-example", "JWT issuer")
	flag.DurationVar(&cfg.jwtExpiry, "jwt-expiry", time.Hour, "JWT lifetime")
	flag.StringVar(&cfg.apiKey, "api-key", "test-api-key", "API key for this local demo")
	flag.StringVar(&cfg.apiKeyHeader, "api-key-header", "X-API-Key", "API-key request header")
	flag.Parse()
	cfg.authMethod = strings.ToLower(cfg.authMethod)
	return cfg
}

func main() {
	cfg := parseFlags()
	a2aClient, err := newClient(cfg)
	if err != nil {
		log.Fatalf("create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	extended, err := a2aClient.GetAuthenticatedExtendedCard(ctx)
	if err != nil {
		log.Fatalf("get authenticated extended card: %v", err)
	}
	fmt.Printf("Authenticated card: %s\n", extended.Name)
	fmt.Printf("Description: %s\n", extended.Description)

	message := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(cfg.message)},
	)
	if cfg.contextID != "" {
		message.ContextID = &cfg.contextID
	}
	result, err := a2aClient.SendMessage(ctx, protocol.SendMessageParams{Message: message})
	if err != nil {
		log.Fatalf("send message: %v", err)
	}

	if response := result.GetMessage(); response != nil {
		for _, part := range response.Parts {
			if text := part.TextContent(); text != "" {
				fmt.Printf("Response: %s\n", text)
			}
		}
		return
	}
	if task := result.GetTask(); task != nil {
		fmt.Printf("Task: %s (%s)\n", task.ID, task.Status.State)
		return
	}
	log.Fatal("server returned an empty SendMessage result")
}

func newClient(cfg config) (*client.A2AClient, error) {
	switch cfg.authMethod {
	case "jwt":
		secret, err := os.ReadFile(cfg.jwtSecretFile)
		if err != nil {
			return nil, fmt.Errorf("read JWT secret file: %w", err)
		}
		if len(secret) < 32 {
			return nil, fmt.Errorf("JWT secret in %s must be at least 32 bytes", cfg.jwtSecretFile)
		}
		return client.NewA2AClient(
			cfg.agentURL,
			client.WithJWTAuth(secret, cfg.jwtAudience, cfg.jwtIssuer, cfg.jwtExpiry),
		)
	case "apikey":
		return client.NewA2AClient(
			cfg.agentURL,
			client.WithAPIKeyAuth(cfg.apiKey, cfg.apiKeyHeader),
		)
	default:
		return nil, fmt.Errorf("unsupported -auth %q: use jwt or apikey", cfg.authMethod)
	}
}
