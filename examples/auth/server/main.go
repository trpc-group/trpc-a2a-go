// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements an A2A server with authentication and push notification authentication.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"flag"

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Config holds server configuration
type Config struct {
	Host          string
	Port          int
	JWTSecretFile string
	JWTSecret     []byte
	JWTAudience   string
	JWTIssuer     string
	APIKeys       map[string]string
	APIKeyHeader  string
	UseHTTPS      bool
	CertFile      string
	KeyFile       string
	EnableOAuth   bool
}

func main() {
	// Parse command line flags
	config := parseFlags()

	// Create a HTTP server mux for both the A2A server and OAuth endpoints
	mux := http.NewServeMux()

	// Start the OAuth mock server if enabled
	var tokenEndpoint string
	if config.EnableOAuth {
		oauthServer := NewMockOAuthServer()
		oauthServer.Start(mux)
		tokenEndpoint = "http://localhost:" + fmt.Sprintf("%d", config.Port) + oauthServer.TokenEndpoint
		log.Printf("OAuth token endpoint: %s", tokenEndpoint)
	}

	// Create a simple echo processor for demonstration purposes
	processor := &echoProcessor{}

	// Create a real task manager with our processor
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Load or generate JWT secret
	if err := loadOrGenerateSecret(config); err != nil {
		log.Fatalf("Failed to setup JWT secret: %v", err)
	}

	// Create JWT auth provider
	jwtProvider := auth.NewJWTAuthProvider(
		config.JWTSecret,
		config.JWTAudience,
		config.JWTIssuer,
		1*time.Hour,
	)

	// Create API key auth provider
	apiKeyProvider := auth.NewAPIKeyAuthProvider(config.APIKeys, config.APIKeyHeader)

	// Create an OAuth2 auth provider if enabled
	var providers []auth.Provider
	providers = append(providers, jwtProvider, apiKeyProvider)

	// Chain the auth providers
	chainProvider := auth.NewChainAuthProvider(providers...)

	// Create agent card with authentication info
	authType := "apiKey,jwt"
	if config.EnableOAuth {
		authType += ",oauth2"
	}

	agentCard := server.AgentCard{
		Name:        "A2A Server with Authentication",
		Description: addressableStr("A demonstration server with JWT and API key authentication"),
		URL:         fmt.Sprintf("http://localhost:%d", config.Port),
		Provider: &server.AgentProvider{
			Name: "Example Provider",
		},
		Version: "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
		},
		Authentication: &server.AgentAuthentication{
			Type:     authType,
			Required: true,
			Config: map[string]interface{}{
				"jwt": map[string]interface{}{
					"audience": config.JWTAudience,
					"issuer":   config.JWTIssuer,
				},
				"apiKey": map[string]interface{}{
					"headerName": config.APIKeyHeader,
				},
			},
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	// Add OAuth2 configuration if enabled
	if config.EnableOAuth {
		if agentCard.Authentication != nil {
			configMap, ok := agentCard.Authentication.Config.(map[string]interface{})
			if ok {
				configMap["oauth2"] = map[string]interface{}{
					"tokenUrl": tokenEndpoint,
					"scopes":   []string{"a2a.read", "a2a.write"},
				}
			}
		}
	}

	// Create the server with authentication
	a2aServer, err := server.NewA2AServer(
		agentCard,
		taskManager,
		server.WithAuthProvider(chainProvider),
		server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Get A2A server http handler and add it to the mux
	mux.Handle("/", a2aServer.Handler())

	// Create an HTTP server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.Host, config.Port),
		Handler: mux,
	}

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, initiating shutdown...", sig)
		cancel()
	}()

	// Start the server in a goroutine
	go func() {
		log.Printf("Starting server on %s:%d...", config.Host, config.Port)
		var err error
		if config.UseHTTPS {
			err = httpServer.ListenAndServeTLS(config.CertFile, config.KeyFile)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Create a token for testing
	token, err := jwtProvider.CreateToken("test-user", nil)
	if err != nil {
		log.Printf("Warning: Failed to create test token: %v", err)
	} else {
		log.Printf("Test JWT token: %s", token)
		printExampleCommands(config.Port, token, config.EnableOAuth, tokenEndpoint)
	}

	// Wait for context cancellation (from signal handler)
	<-ctx.Done()

	// Perform graceful shutdown with a 5-second timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(), 5*time.Second,
	)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error during server shutdown: %v", err)
	}
	log.Println("Server shutdown complete")
}

// parseFlags parses command line flags and returns a configuration
func parseFlags() *Config {
	config := &Config{
		APIKeys: map[string]string{
			"test-api-key": "test-user",
		},
		APIKeyHeader: "X-API-Key",
	}

	flag.StringVar(&config.Host, "host", "localhost", "Host address to bind to")
	flag.IntVar(&config.Port, "port", 8080, "Port to listen on")
	flag.StringVar(&config.JWTSecretFile, "jwt-secret-file", "jwt-secret.key", "File to store JWT secret")
	flag.StringVar(&config.JWTAudience, "jwt-audience", "a2a-server", "JWT audience claim")
	flag.StringVar(&config.JWTIssuer, "jwt-issuer", "example", "JWT issuer claim")
	flag.BoolVar(&config.UseHTTPS, "https", false, "Use HTTPS")
	flag.StringVar(&config.CertFile, "cert", "server.crt", "TLS certificate file (for HTTPS)")
	flag.StringVar(&config.KeyFile, "key", "server.key", "TLS key file (for HTTPS)")
	flag.BoolVar(&config.EnableOAuth, "enable-oauth", true, "Enable OAuth2 mock server")

	flag.Parse()
	return config
}

// loadOrGenerateSecret loads a JWT secret from file or generates and saves a new one
func loadOrGenerateSecret(config *Config) error {
	// Try to load existing secret
	data, err := os.ReadFile(config.JWTSecretFile)
	if err == nil && len(data) >= 32 {
		log.Printf("Loaded JWT secret from %s", config.JWTSecretFile)
		config.JWTSecret = data
		return nil
	}

	// Generate new secret
	config.JWTSecret = make([]byte, 32)
	if _, err := rand.Read(config.JWTSecret); err != nil {
		return fmt.Errorf("failed to generate JWT secret: %w", err)
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(config.JWTSecretFile)
	if dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("failed to create directory for JWT secret: %w", err)
		}
	}

	// Save for future use with tight permissions
	if err := os.WriteFile(config.JWTSecretFile, config.JWTSecret, 0600); err != nil {
		log.Printf("Warning: Could not save JWT secret to %s: %v", config.JWTSecretFile, err)
	} else {
		log.Printf("Generated and saved new JWT secret to %s", config.JWTSecretFile)
	}

	return nil
}

// printExampleCommands prints example curl commands for testing
func printExampleCommands(port int, token string, enableOAuth bool, tokenEndpoint string) {
	log.Printf("Example curl commands:")

	// JWT example
	log.Printf("Using JWT authentication:")
	log.Printf("curl -X POST http://localhost:%d -H 'Content-Type: application/json' "+
		"-H 'Authorization: Bearer %s' "+
		"-d '{\"jsonrpc\":\"2.0\",\"method\":\"tasks/send\",\"id\":1,"+
		"\"params\":{\"id\":\"task1\",\"message\":{\"role\":\"user\","+
		"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'", port, token)

	// API key example
	log.Printf("\nUsing API key authentication:")
	log.Printf("curl -X POST http://localhost:%d -H 'Content-Type: application/json' "+
		"-H 'X-API-Key: test-api-key' "+
		"-d '{\"jsonrpc\":\"2.0\",\"method\":\"tasks/send\",\"id\":1,"+
		"\"params\":{\"id\":\"task1\",\"message\":{\"role\":\"user\","+
		"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'", port)

	// OAuth2 example if enabled
	if enableOAuth {
		log.Printf("\nUsing OAuth2 authentication:")
		log.Printf("Step 1: Get OAuth2 token:")
		log.Printf("curl -X POST %s -u my-client-id:my-client-secret "+
			"-d 'grant_type=client_credentials&scope=a2a.read a2a.write'", tokenEndpoint)
		log.Printf("\nStep 2: Use the token with the A2A API:")
		log.Printf("curl -X POST http://localhost:%d -H 'Content-Type: application/json' "+
			"-H 'Authorization: Bearer <access_token_from_step_1>' "+
			"-d '{\"jsonrpc\":\"2.0\",\"method\":\"tasks/send\",\"id\":1,"+
			"\"params\":{\"id\":\"task1\",\"message\":{\"role\":\"user\","+
			"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'", port)
	}

	// Agent card example
	log.Printf("\nFetch agent card:")
	log.Printf("curl http://localhost:%d/.well-known/agent.json", port)

	// JWKS endpoint example
	log.Printf("\nFetch JWKS endpoint:")
	log.Printf("curl http://localhost:%d/.well-known/jwks.json", port)
}

// echoProcessor is a simple processor that echoes user messages
type echoProcessor struct{}

func (p *echoProcessor) Process(
	ctx context.Context,
	taskID string,
	msg protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	// Create a concatenated string of all text parts
	var responseText string
	for _, part := range msg.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			responseText += textPart.Text + " "
		}
	}

	// Create response message
	responseMsg := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Echo: %s", responseText)),
		},
	}

	// Update the task status to completed with our response
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, responseMsg); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	return nil
}

func addressableStr(s string) *string {
	return &s
}
