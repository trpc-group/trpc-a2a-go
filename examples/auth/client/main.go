// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main provides example code for using different authentication methods with the A2A client.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/clientcredentials"
	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// config holds the client configuration options.
type config struct {
	AuthMethod string
	AgentURL   string
	Timeout    time.Duration

	// JWT Auth options
	JWTSecret     string
	JWTSecretFile string
	JWTAudience   string
	JWTIssuer     string
	JWTExpiry     time.Duration

	// API Key options
	APIKey       string
	APIKeyHeader string

	// OAuth2 options
	OAuth2ClientID     string
	OAuth2ClientSecret string
	OAuth2TokenURL     string
	OAuth2Scopes       string

	// Task options
	TaskID      string
	TaskMessage string
	SessionID   string
}

func main() {
	config := parseFlags()

	if config.AuthMethod == "" {
		flag.Usage()
		return
	}

	var a2aClient *client.A2AClient
	var err error

	// Create client with the specified authentication method
	switch config.AuthMethod {
	case "jwt":
		a2aClient, err = createJWTClient(config)
	case "apikey":
		a2aClient, err = createAPIKeyClient(config)
	case "oauth2":
		a2aClient, err = createOAuth2Client(config)
	default:
		fmt.Printf("Unknown authentication method: %s\n", config.AuthMethod)
		return
	}

	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Create a simple task to test authentication
	textPart := protocol.NewTextPart(config.TaskMessage)
	message := protocol.NewMessage(protocol.MessageRoleUser, []protocol.Part{textPart})

	// Prepare message parameters
	params := protocol.SendMessageParams{
		Message: message,
	}

	// Add context ID if session ID is provided
	if config.SessionID != "" {
		// In the new protocol, we use contextID instead of sessionID
		params.Message.ContextID = &config.SessionID
	}

	// Send the message
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	result, err := a2aClient.SendMessage(ctx, params)

	if err != nil {
		log.Fatalf("Failed to send message: %v", err)
	}

	// Handle the response based on its type
	switch response := result.Result.(type) {
	case *protocol.Message:
		fmt.Printf("Message Response: %s\n", response.MessageID)
		if response.ContextID != nil {
			fmt.Printf("Context ID: %s\n", *response.ContextID)
		}
		// Print message parts
		for _, part := range response.Parts {
			if textPart, ok := part.(*protocol.TextPart); ok {
				fmt.Printf("Response: %s\n", textPart.Text)
			}
		}

	case *protocol.Task:
		fmt.Printf("Task ID: %s, Status: %s\n", response.ID, response.Status.State)
		if response.ContextID != "" {
			fmt.Printf("Context ID: %s\n", response.ContextID)
		}

		// For demonstration purposes, get the task status
		taskQuery := protocol.TaskQueryParams{
			ID: response.ID,
		}

		updatedTask, err := a2aClient.GetTasks(ctx, taskQuery)
		if err != nil {
			log.Fatalf("Failed to get task: %v", err)
		}

		fmt.Printf("Updated task status: %s\n", updatedTask.Status.State)

	default:
		fmt.Printf("Unknown response type: %T\n", response)
	}
}

// parseFlags parses command-line flags and returns a Config.
func parseFlags() config {
	var config config

	// Basic options
	flag.StringVar(&config.AuthMethod, "auth", "jwt", "Authentication method (jwt, apikey, oauth2)")
	flag.StringVar(&config.AgentURL, "url", "http://localhost:8080/", "Target A2A agent URL")
	flag.DurationVar(&config.Timeout, "timeout", 60*time.Second, "Request timeout")

	// JWT options
	flag.StringVar(&config.JWTSecret, "jwt-secret", "my-secret-key", "JWT secret key")
	flag.StringVar(&config.JWTSecretFile, "jwt-secret-file", "../server/jwt-secret.key", "File containing JWT secret key")
	flag.StringVar(&config.JWTAudience, "jwt-audience", "a2a-server", "JWT audience")
	flag.StringVar(&config.JWTIssuer, "jwt-issuer", "example", "JWT issuer")
	flag.DurationVar(&config.JWTExpiry, "jwt-expiry", 1*time.Hour, "JWT expiration time")

	// API Key options
	flag.StringVar(&config.APIKey, "api-key", "test-api-key", "API key")
	flag.StringVar(&config.APIKeyHeader, "api-key-header", "X-API-Key", "API key header name")

	// OAuth2 options
	flag.StringVar(&config.OAuth2ClientID, "oauth2-client-id", "my-client-id", "OAuth2 client ID")
	flag.StringVar(&config.OAuth2ClientSecret, "oauth2-client-secret", "my-client-secret", "OAuth2 client secret")
	flag.StringVar(&config.OAuth2TokenURL, "oauth2-token-url", "", "OAuth2 token URL (default: derived from agent URL)")
	flag.StringVar(&config.OAuth2Scopes, "oauth2-scopes", "a2a.read,a2a.write", "OAuth2 scopes (comma-separated)")

	// Task options
	flag.StringVar(&config.TaskID, "task-id", "auth-test-task", "ID for the task to send")
	flag.StringVar(&config.TaskMessage, "message", "Hello, this is an authenticated request", "Message to send")
	flag.StringVar(&config.SessionID, "session-id", "", "Optional session ID for the task")

	flag.Parse()

	return config
}

// getJWTSecret retrieves the JWT secret from either the direct key or a file.
func getJWTSecret(config config) ([]byte, error) {
	// If a secret file is provided, read from it
	if config.JWTSecretFile != "" {
		secret, err := os.ReadFile(config.JWTSecretFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read JWT secret file: %w", err)
		}
		return secret, nil
	}

	// Otherwise use the direct secret value
	return []byte(config.JWTSecret), nil
}

// createJWTClient creates an A2A client with JWT authentication.
func createJWTClient(config config) (*client.A2AClient, error) {
	secret, err := getJWTSecret(config)
	if err != nil {
		return nil, err
	}

	return client.NewA2AClient(
		config.AgentURL,
		client.WithJWTAuth(secret, config.JWTAudience, config.JWTIssuer, config.JWTExpiry),
	)
}

// createAPIKeyClient creates an A2A client with API key authentication.
func createAPIKeyClient(config config) (*client.A2AClient, error) {
	return client.NewA2AClient(
		config.AgentURL,
		client.WithAPIKeyAuth(config.APIKey, config.APIKeyHeader),
	)
}

// createOAuth2Client creates an A2A client with OAuth2 authentication.
func createOAuth2Client(config config) (*client.A2AClient, error) {
	// Method 1: Using client credentials flow
	return createOAuth2ClientCredentialsClient(config)

	// Alternative methods:
	// return createOAuth2TokenSourceClient(config)
	// return createCustomOAuth2Client(config)
}

// createOAuth2ClientCredentialsClient creates a client using OAuth2 client credentials flow.
func createOAuth2ClientCredentialsClient(config config) (*client.A2AClient, error) {
	// Determine token URL if not specified
	tokenURL := config.OAuth2TokenURL
	if tokenURL == "" {
		tokenURL = getOAuthTokenURL(config.AgentURL)
	}

	// Parse scopes
	scopes := []string{}
	if config.OAuth2Scopes != "" {
		for _, scope := range strings.Split(config.OAuth2Scopes, ",") {
			scopes = append(scopes, strings.TrimSpace(scope))
		}
	}

	return client.NewA2AClient(
		config.AgentURL,
		client.WithOAuth2ClientCredentials(config.OAuth2ClientID, config.OAuth2ClientSecret, tokenURL, scopes),
	)
}

// createOAuth2TokenSourceClient creates a client using a custom OAuth2 token source.
func createOAuth2TokenSourceClient(config config) (*client.A2AClient, error) {
	// Extract the OAuth token URL from agentURL
	tokenURL := getOAuthTokenURL(config.AgentURL)

	// Example with password credentials grant
	config.OAuth2TokenURL = tokenURL
	config.OAuth2Scopes = "a2a.read,a2a.write"

	return createOAuth2ClientCredentialsClient(config)
}

// createCustomOAuth2Client creates a client with a completely custom OAuth2 provider.
func createCustomOAuth2Client(config config) (*client.A2AClient, error) {
	// Extract the OAuth token URL from agentURL
	tokenURL := getOAuthTokenURL(config.AgentURL)

	// Create a client credentials config
	ccConfig := &clientcredentials.Config{
		ClientID:     config.OAuth2ClientID,
		ClientSecret: config.OAuth2ClientSecret,
		TokenURL:     tokenURL,
		Scopes:       []string{config.OAuth2Scopes},
	}

	// Create a custom OAuth2 provider
	provider := auth.NewOAuth2ClientCredentialsProvider(
		ccConfig.ClientID,
		ccConfig.ClientSecret,
		ccConfig.TokenURL,
		ccConfig.Scopes,
	)

	// Use the custom provider
	return client.NewA2AClient(
		config.AgentURL,
		client.WithAuthProvider(provider),
	)
}

// getOAuthTokenURL is a helper function to get the OAuth token URL based on agent URL.
func getOAuthTokenURL(agentURL string) string {
	tokenURL := ""
	if agentURL == "http://localhost:8080/" {
		tokenURL = "http://localhost:8080/oauth2/token"
	} else {
		// Try to adapt to a different port
		// This is a simple adaptation, not fully robust
		tokenURL = agentURL + "oauth2/token"
		if tokenURL[len(tokenURL)-1] == '/' {
			tokenURL = tokenURL[:len(tokenURL)-1]
		}
	}
	fmt.Printf("Using OAuth2 token URL: %s\n", tokenURL)
	return tokenURL
}
