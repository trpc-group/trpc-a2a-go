// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

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
	// Parse command-line flags.
	config := parseFlags()

	var client *client.A2AClient
	var err error

	switch config.AuthMethod {
	case "jwt":
		client, err = createJWTClient(config)
	case "apikey":
		client, err = createAPIKeyClient(config)
	case "oauth2":
		client, err = createOAuth2Client(config)
	default:
		fmt.Printf("Unknown authentication method: %s\n", config.AuthMethod)
		return
	}

	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Create the message to send using the new constructor.
	message := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(config.TaskMessage)},
	)

	// Create message parameters using the new SendMessageParams structure.
	params := protocol.SendMessageParams{
		Message: message,
	}

	// Send the message
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	result, err := client.SendMessage(ctx, params)
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

		updatedTask, err := client.GetTasks(ctx, taskQuery)
		if err != nil {
			log.Fatalf("Failed to get task: %v", err)
		}

		fmt.Printf("Updated task status: %s\n", updatedTask.Status.State)

	default:
		fmt.Printf("Unknown response type: %T\n", response)
	}

}

// JWT Authentication
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

func getJWTSecret(config config) ([]byte, error) {
	if config.JWTSecretFile != "" {
		return os.ReadFile(config.JWTSecretFile)
	}
	return []byte(config.JWTSecret), nil
}

// API Key Authentication
func createAPIKeyClient(config config) (*client.A2AClient, error) {
	return client.NewA2AClient(
		config.AgentURL,
		client.WithAPIKeyAuth(config.APIKey, config.APIKeyHeader),
	)
}

// OAuth2 Client Credentials
func createOAuth2Client(config config) (*client.A2AClient, error) {
	// Get OAuth2 token URL
	tokenURL := config.OAuth2TokenURL
	if tokenURL == "" {
		tokenURL = config.AgentURL + "oauth2/token"
		if tokenURL[len(tokenURL)-1] == '/' {
			tokenURL = tokenURL[:len(tokenURL)-1]
		}
	}
	fmt.Printf("Using OAuth2 token URL: %s\n", tokenURL)

	// Parse scopes
	scopes := []string{}
	if config.OAuth2Scopes != "" {
		for _, scope := range strings.Split(config.OAuth2Scopes, ",") {
			scopes = append(scopes, strings.TrimSpace(scope))
		}
	}

	return client.NewA2AClient(
		config.AgentURL,
		client.WithOAuth2ClientCredentials(
			config.OAuth2ClientID,
			config.OAuth2ClientSecret,
			tokenURL,
			scopes,
		),
	)
}

func parseFlags() config {
	var config config

	// Basic options
	flag.StringVar(&config.AuthMethod, "auth", "jwt", "Authentication method (jwt, apikey, oauth2)")
	flag.StringVar(&config.AgentURL, "url", "http://localhost:8080/", "Target A2A agent URL")
	flag.DurationVar(&config.Timeout, "timeout", 60*time.Second, "Request timeout")

	// JWT options
	flag.StringVar(&config.JWTSecret, "jwt-secret", "my-secret-key", "JWT secret key")
	flag.StringVar(&config.JWTSecretFile, "jwt-secret-file", "", "File containing JWT secret key")
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
