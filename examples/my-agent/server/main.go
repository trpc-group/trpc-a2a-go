// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements my A2A server example.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	AgentURL      string
	JWTSecretFile string
	JWTSecret     []byte
	JWTAudience   string
	JWTIssuer     string
	APIKeys       map[string]string
	APIKeyHeader  string
	UseHTTPS      bool
	CertFile      string
	KeyFile       string
}

// Implement the MessageProcessor interface
type myMessageProcessor struct {
	// Add your custom fields here
}

func (p *myMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Extract text from the incoming message
	text := extractTextFromMessage(message)

	// Process the text (example: reverse it)
	result := reverseString(text)

	// Return a simple response message
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart("Processed: " + result)},
	)

	return &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}, nil
}

func extractTextFromMessage(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}

// Helper function to create bool pointers
func boolPtr(b bool) *bool {
	return &b
}

func securitySchemeInPtr(in server.SecuritySchemeIn) *server.SecuritySchemeIn {
	return &in
}

func main() {

	// Parse command line flags
	config := parseFlags()

	// Create a HTTP server mux for both the A2A server and OAuth endpoints
	mux := http.NewServeMux()

	// Start the OAuth mock server
	var tokenEndpoint string

	oauthServer := newMockOAuthServer()
	oauthServer.Start(mux)
	tokenEndpoint = config.AgentURL + oauthServer.TokenEndpoint
	log.Printf("OAuth token endpoint: %s", tokenEndpoint)

	// Create a JWT authentication provider
	if err := loadOrGenerateSecret(config); err != nil {
		log.Fatalf("Failed to setup JWT secret: %v", err)
	}
	jwtProvider := auth.NewJWTAuthProvider(
		config.JWTSecret,
		config.JWTAudience,
		config.JWTIssuer,
		1*time.Hour,
	)

	// Or create an API key authentication provider
	apiKeys := map[string]string{
		"api-key-1": "user1",
		"api-key-2": "user2",
	}
	apiKeyProvider := auth.NewAPIKeyAuthProvider(apiKeys, "X-API-Key")

	// OAuth2 token validation provider
	oauth2Provider := auth.NewOAuth2AuthProviderWithConfig(
		nil,   // No config needed for simple validation
		"",    // No userinfo endpoint for this example
		"sub", // Default subject field for identifying users
	)

	// Chain multiple authentication methods
	chainProvider := auth.NewChainAuthProvider(
		jwtProvider,
		apiKeyProvider,
		oauth2Provider,
	)

	// Create the agent card
	agentCard := server.AgentCard{
		Name:        "My Agent",
		Description: "Agent description",
		URL:         config.AgentURL,
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "Provider name",
		},
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
		DefaultInputModes:  []string{protocol.KindText},
		DefaultOutputModes: []string{protocol.KindText},
		Skills: []server.AgentSkill{
			{
				ID:          "text_processing",
				Name:        "Text Processing",
				Description: stringPtr("Process and transform text input"),
				InputModes:  []string{protocol.KindText},
				OutputModes: []string{protocol.KindText},
			},
		},
	}

	// Create the task processor
	processor := &myMessageProcessor{}

	// Create task manager, inject processor
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create an authenticator for push notifications
	notifAuth := auth.NewPushNotificationAuthenticator()

	// Generate a key pair
	if err := notifAuth.GenerateKeyPair(); err != nil {
		// Handle error
	}

	// Expose JWKS endpoint
	http.HandleFunc("/.well-known/jwks.json", notifAuth.HandleJWKS)

	// Create the server with authentication
	srv, err := server.NewA2AServer(
		agentCard,
		taskManager,
		server.WithAuthProvider(chainProvider),
		// Enable JWKS endpoint when creating the server
		server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
	)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Get A2A server http handler and add it to the mux
	mux.Handle("/", srv.Handler())

	// Create an HTTP server
	httpServer := &http.Server{
		Addr:    config.AgentURL,
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
		log.Printf("Starting server on %s...", config.AgentURL)
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

func parseFlags() *config {
	config := &config{
		APIKeys: map[string]string{
			"test-api-key": "test-user",
		},
		APIKeyHeader: "X-API-Key",
	}

	flag.StringVar(&config.AgentURL, "url", "localhost:8080", "Server URL")
	flag.StringVar(&config.JWTSecretFile, "jwt-secret-file", "jwt-secret.key", "File to store JWT secret")
	flag.StringVar(&config.JWTAudience, "jwt-audience", "a2a-server", "JWT audience claim")
	flag.StringVar(&config.JWTIssuer, "jwt-issuer", "example", "JWT issuer claim")
	flag.BoolVar(&config.UseHTTPS, "https", false, "Use HTTPS")
	flag.StringVar(&config.CertFile, "cert", "server.crt", "TLS certificate file (for HTTPS)")
	flag.StringVar(&config.KeyFile, "key", "server.key", "TLS key file (for HTTPS)")

	flag.Parse()
	return config
}

// loadOrGenerateSecret loads a JWT secret from file or generates and saves a new one
func loadOrGenerateSecret(config *config) error {
	data, err := os.ReadFile(config.JWTSecretFile)
	if err == nil && len(data) >= 32 {
		log.Printf("Loaded JWT secret from %s", config.JWTSecretFile)
		config.JWTSecret = data
		return nil
	}

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	if err != nil {
		return err
	}

	if err := os.WriteFile(config.JWTSecretFile, secret, 0600); err != nil {
		return err
	}

	log.Printf("Generated new JWT secret and saved to %s", config.JWTSecretFile)
	config.JWTSecret = secret
	return nil
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
