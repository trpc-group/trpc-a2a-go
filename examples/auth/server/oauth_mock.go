// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// MockOAuthServer implements a simple OAuth2 server for demonstration purposes.
type MockOAuthServer struct {
	// ValidCredentials maps client_id to client_secret
	ValidCredentials map[string]string
	// TokenEndpoint is the path for token requests (e.g., "/oauth2/token")
	TokenEndpoint string
}

// NewMockOAuthServer creates a new mock OAuth2 server.
func NewMockOAuthServer() *MockOAuthServer {
	return &MockOAuthServer{
		ValidCredentials: map[string]string{
			"my-client-id": "my-client-secret",
		},
		TokenEndpoint: "/oauth2/token",
	}
}

// Start initializes the OAuth server handlers and starts the server.
func (m *MockOAuthServer) Start(mux *http.ServeMux) {
	mux.HandleFunc(m.TokenEndpoint, m.handleTokenRequest)
	log.Printf("Mock OAuth2 server endpoint available at: %s", m.TokenEndpoint)
	log.Printf("Use client_id: 'my-client-id' and client_secret: 'my-client-secret'")
}

// handleTokenRequest processes OAuth2 token requests.
func (m *MockOAuthServer) handleTokenRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseForm()
	if err != nil {
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
	AccessToken string   `json:"access_token"`
	TokenType   string   `json:"token_type"`
	ExpiresIn   int      `json:"expires_in"`
	Scope       string   `json:"scope,omitempty"`
	ClientID    string   `json:"client_id"`
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