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
	"encoding/json"
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

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// config holds server configuration
type config struct {
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
		oauthServer := newMockOAuthServer()
		oauthServer.Start(mux)
		tokenEndpoint = "http://localhost:" + fmt.Sprintf("%d", config.Port) + oauthServer.TokenEndpoint
		log.Printf("OAuth token endpoint: %s", tokenEndpoint)
	}

	// Create a simple echo processor for demonstration purposes
	processor := &echoMessageProcessor{}

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

	// Add OAuth2 provider if enabled
	if config.EnableOAuth {
		// For server-side token validation, we use NewOAuth2AuthProviderWithConfig
		// which is designed to validate tokens rather than generate them
		oauth2Provider := auth.NewOAuth2AuthProviderWithConfig(
			nil,   // No config needed for simple validation
			"",    // No userinfo endpoint for this example
			"sub", // Default subject field
		)
		providers = append(providers, oauth2Provider)
		log.Printf("Added OAuth2 authentication provider for token validation")
	}

	// Chain the auth providers
	chainProvider := auth.NewChainAuthProvider(providers...)

	// Create agent card with authentication info
	authType := "apiKey,jwt"
	if config.EnableOAuth {
		authType += ",oauth2"
	}

	agentCard := server.AgentCard{
		Name:        "A2A Server with Authentication",
		Description: "A demonstration server with JWT and API key authentication",
		URL:         fmt.Sprintf("http://localhost:%d", config.Port),
		Provider: &server.AgentProvider{
			Organization: "Example Provider",
		},
		Version: "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		SecuritySchemes: map[string]server.SecurityScheme{
			"apiKey": {
				Type:        "apiKey",
				Description: stringPtr("API key authentication"),
				Name:        stringPtr(config.APIKeyHeader),
				In:          securitySchemeInPtr(server.SecuritySchemeInHeader),
			},
			"jwt": {
				Type:         "http",
				Description:  stringPtr("JWT Bearer token authentication"),
				Scheme:       stringPtr("bearer"),
				BearerFormat: stringPtr("JWT"),
			},
		},
		Security: []map[string][]string{
			{"apiKey": {}},
			{"jwt": {}},
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "echo",
				Name:        "Echo Service",
				Description: stringPtr("Echoes back the input text with authentication"),
				Tags:        []string{"text", "echo", "auth"},
				Examples:    []string{"Hello, world!"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// Create the server with authentication
	a2aServer, err := server.NewA2AServer(
		agentCard,
		taskManager,
		server.WithAuthProvider(chainProvider),
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
func parseFlags() *config {
	config := &config{
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
func loadOrGenerateSecret(config *config) error {
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
		"-d '{\"jsonrpc\":\"2.0\",\"method\":\"message/send\",\"id\":1,"+
		"\"params\":{\"message\":{\"role\":\"user\","+
		"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'", port, token)

	// API key example
	log.Printf("\nUsing API key authentication:")
	log.Printf("curl -X POST http://localhost:%d -H 'Content-Type: application/json' "+
		"-H 'X-API-Key: test-api-key' "+
		"-d '{\"jsonrpc\":\"2.0\",\"method\":\"message/send\",\"id\":1,"+
		"\"params\":{\"message\":{\"role\":\"user\","+
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
			"-d '{\"jsonrpc\":\"2.0\",\"method\":\"message/send\",\"id\":1,"+
			"\"params\":{\"message\":{\"role\":\"user\","+
			"\"parts\":[{\"type\":\"text\",\"text\":\"Hello, world!\"}]}}}'", port)
	}

	// Agent card example
	log.Printf("\nFetch agent card:")
	log.Printf("curl http://localhost:%d/.well-known/agent.json", port)
}

// echoMessageProcessor is a simple processor that echoes user messages
type echoMessageProcessor struct{}

func (p *echoMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Create a concatenated string of all text parts
	var responseText string
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			responseText += textPart.Text + " "
		}
	}

	// Create response message
	responseMsg := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Echo: %s", responseText)),
		},
	)

	// Return the response message directly
	return &taskmanager.MessageProcessingResult{
		Result: &responseMsg,
	}, nil
}

func addressableStr(s string) *string {
	return &s
}

// mockOAuthServer implements a simple OAuth2 server for demonstration purposes.
type mockOAuthServer struct {
	// ValidCredentials maps client_id to client_secret
	ValidCredentials map[string]string
	// TokenEndpoint is the path for token requests (e.g., "/oauth2/token")
	TokenEndpoint string
}

// newMockOAuthServer creates a new mock OAuth2 server.
func newMockOAuthServer() *mockOAuthServer {
	return &mockOAuthServer{
		ValidCredentials: map[string]string{
			"my-client-id": "my-client-secret",
		},
		TokenEndpoint: "/oauth2/token",
	}
}

// Start initializes the OAuth server handlers and starts the server.
func (m *mockOAuthServer) Start(mux *http.ServeMux) {
	mux.HandleFunc(m.TokenEndpoint, m.handleTokenRequest)
	log.Printf("Mock OAuth2 server endpoint available at: %s", m.TokenEndpoint)
	log.Printf("Use client_id: 'my-client-id' and client_secret: 'my-client-secret'")
}

// handleTokenRequest processes OAuth2 token requests.
func (m *mockOAuthServer) handleTokenRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	// Get grant type
	grantType := r.FormValue("grant_type")
	if grantType != "client_credentials" {
		http.Error(w, "Unsupported grant type", http.StatusBadRequest)
		return
	}

	// Get client credentials
	clientID, clientSecret := getClientCredentials(r)
	if clientID == "" || clientSecret == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="OAuth2 Server"`)
		http.Error(w, "Missing client credentials", http.StatusUnauthorized)
		return
	}

	// Validate credentials
	validSecret, ok := m.ValidCredentials[clientID]
	if !ok || validSecret != clientSecret {
		http.Error(w, "Invalid client credentials", http.StatusUnauthorized)
		return
	}

	// Get requested scopes
	scopeStr := r.FormValue("scope")
	scopes := []string{}
	if scopeStr != "" {
		scopes = strings.Split(scopeStr, " ")
	}

	// Generate token response
	token := generateToken(clientID, scopes)

	// Return the token
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(token)
}

// getClientCredentials extracts client credentials from the request.
// Supports both Basic auth and form parameters.
func getClientCredentials(r *http.Request) (string, string) {
	// Try Basic auth first
	clientID, clientSecret, ok := r.BasicAuth()
	if ok && clientID != "" && clientSecret != "" {
		return clientID, clientSecret
	}

	// Try form parameters
	return r.FormValue("client_id"), r.FormValue("client_secret")
}

// TokenResponse represents an OAuth2 token response.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
	ClientID    string `json:"client_id"`
}

// generateToken creates a mock access token.
func generateToken(clientID string, scopes []string) TokenResponse {
	// In a real implementation, this would be a proper signed JWT
	return TokenResponse{
		AccessToken: "mock-access-token-" + clientID,
		TokenType:   "Bearer",
		ExpiresIn:   3600,
		Scope:       strings.Join(scopes, " "),
		ClientID:    clientID,
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func stringPtr(s string) *string {
	return &s
}

func securitySchemeInPtr(in server.SecuritySchemeIn) *server.SecuritySchemeIn {
	return &in
}
