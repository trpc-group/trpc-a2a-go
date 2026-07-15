// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command server runs an A2A echo agent protected by JWT or API-key authentication.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/auth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

type config struct {
	host          string
	port          int
	agentURL      string
	jwtSecretFile string
	jwtAudience   string
	jwtIssuer     string
	apiKey        string
	apiKeyHeader  string
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.host, "host", "localhost", "address to listen on")
	flag.IntVar(&cfg.port, "port", 8080, "port to listen on")
	flag.StringVar(&cfg.agentURL, "agent-url", "", "public A2A endpoint (default http://localhost:<port>)")
	flag.StringVar(&cfg.jwtSecretFile, "jwt-secret-file", "jwt-secret.key", "shared JWT secret file for this local demo")
	flag.StringVar(&cfg.jwtAudience, "jwt-audience", "a2a-server", "required JWT audience")
	flag.StringVar(&cfg.jwtIssuer, "jwt-issuer", "auth-example", "required JWT issuer")
	flag.StringVar(&cfg.apiKey, "api-key", "test-api-key", "accepted API key for this local demo")
	flag.StringVar(&cfg.apiKeyHeader, "api-key-header", "X-API-Key", "API-key request header")
	flag.Parse()
	if cfg.agentURL == "" {
		cfg.agentURL = fmt.Sprintf("http://localhost:%d", cfg.port)
	}
	return cfg
}

func main() {
	cfg := parseFlags()
	secret, err := loadOrGenerateSecret(cfg.jwtSecretFile)
	if err != nil {
		log.Fatalf("prepare JWT secret: %v", err)
	}

	processor := echoProcessor{}
	tm, err := memory.NewTaskManager(processor)
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}
	defer tm.Close()

	jwtProvider := auth.NewJWTAuthProvider(secret, cfg.jwtAudience, cfg.jwtIssuer, time.Hour)
	apiKeyProvider := auth.NewAPIKeyAuthProvider(
		map[string]string{cfg.apiKey: "api-key-client"}, cfg.apiKeyHeader,
	)
	provider := auth.NewChainAuthProvider(jwtProvider, apiKeyProvider)

	streaming := false
	stateHistory := false
	extendedCard := true
	scheme := "bearer"
	bearerFormat := "JWT"
	apiKeyLocation := protocol.SecuritySchemeInHeader
	skillDescription := "Echoes authenticated text requests."
	card := protocol.AgentCard{
		Name:        "Authenticated Echo Agent",
		Description: "JWT and API-key authentication example.",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             cfg.agentURL,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Version: "1.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:              &streaming,
			StateTransitionHistory: &stateHistory,
			ExtendedAgentCard:      &extendedCard,
		},
		SecuritySchemes: map[string]protocol.SecurityScheme{
			"jwt": {
				Type:         protocol.SecuritySchemeTypeHTTP,
				Scheme:       &scheme,
				BearerFormat: &bearerFormat,
			},
			"apiKey": {
				Type: protocol.SecuritySchemeTypeAPIKey,
				Name: &cfg.apiKeyHeader,
				In:   &apiKeyLocation,
			},
		},
		SecurityRequirements: protocol.SecurityRequirements{
			{"jwt": {}},
			{"apiKey": {}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:          "echo",
			Name:        "Authenticated Echo",
			Description: &skillDescription,
			Tags:        []string{"echo", "authentication"},
			Examples:    []string{"Hello from an authenticated client"},
			InputModes:  []string{"text/plain"},
			OutputModes: []string{"text/plain"},
		}},
	}

	srv, err := server.NewA2AServer(
		tm,
		server.WithAgentCard(card),
		server.WithAuthProvider(provider),
		server.WithAuthenticatedExtendedCardHandler(
			func(ctx context.Context, base protocol.AgentCard) (protocol.AgentCard, error) {
				user, ok := ctx.Value(auth.AuthUserKey).(*auth.User)
				if !ok || user == nil {
					return protocol.AgentCard{}, errors.New("authenticated user missing from context")
				}
				base.Description = fmt.Sprintf("Authenticated extended card for %s.", user.ID)
				return base, nil
			},
		),
	)
	if err != nil {
		log.Fatalf("create A2A server: %v", err)
	}

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.host, cfg.port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("agent listening at %s", cfg.agentURL)
		log.Printf("JWT secret ready at %s; credentials are not printed", cfg.jwtSecretFile)
		serverErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
		return
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

type echoProcessor struct{}

func (echoProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	var text strings.Builder
	for _, part := range ec.Message.Parts {
		text.WriteString(part.TextContent())
	}
	reply := strings.TrimSpace(text.String())
	if reply == "" {
		reply = "input message contained no text"
	}
	if err := handle.Reply(protocol.NewAgentText("Echo: " + reply)); err != nil {
		return nil, err
	}
	return handle.Events(), nil
}

func loadOrGenerateSecret(path string) ([]byte, error) {
	secret, err := os.ReadFile(path)
	if err == nil {
		if len(secret) < 32 {
			return nil, fmt.Errorf("JWT secret in %s must be at least 32 bytes", path)
		}
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read JWT secret: %w", err)
	}

	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate JWT secret: %w", err)
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create JWT secret directory: %w", err)
		}
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("write JWT secret: %w", err)
	}
	return secret, nil
}
