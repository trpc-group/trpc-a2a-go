// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-a2a-go/auth"
)

func TestPushNotifAuth_GenerateKeyPair(t *testing.T) {
	authenticator := auth.NewPushNotificationAuthenticator()

	err := authenticator.GenerateKeyPair()
	require.NoError(t, err, "Key pair generation should succeed")

	// Attempt to sign payload to verify key generation worked
	payload := []byte("test-payload")
	token, err := authenticator.SignPayload(payload)
	require.NoError(t, err, "Should be able to sign payload after key generation")
	require.NotEmpty(t, token, "Signed token should not be empty")
}

func TestPushNotifAuth_SignPayload(t *testing.T) {
	authenticator := auth.NewPushNotificationAuthenticator()

	// Should fail without key generation
	t.Run("SignPayload_NoKey", func(t *testing.T) {
		payload := []byte("test-payload")
		token, err := authenticator.SignPayload(payload)
		assert.Error(t, err, "Signing should fail without key generation")
		assert.Empty(t, token, "Token should be empty when signing fails")
	})

	// Generate key pair
	err := authenticator.GenerateKeyPair()
	require.NoError(t, err, "Key pair generation should succeed")

	// Should succeed with valid payload
	t.Run("SignPayload_Success", func(t *testing.T) {
		payload := []byte("test-payload")
		token, err := authenticator.SignPayload(payload)
		assert.NoError(t, err, "Signing should succeed with valid key")
		assert.NotEmpty(t, token, "Token should not be empty")
	})

	// Different payloads should produce different tokens
	t.Run("SignPayload_DifferentPayloads", func(t *testing.T) {
		payload1 := []byte("test-payload-1")
		payload2 := []byte("test-payload-2")

		token1, err := authenticator.SignPayload(payload1)
		require.NoError(t, err)

		token2, err := authenticator.SignPayload(payload2)
		require.NoError(t, err)

		assert.NotEqual(t, token1, token2, "Different payloads should produce different tokens")
	})
}

func TestPushNotifAuth_HandleJWKS(t *testing.T) {
	authenticator := auth.NewPushNotificationAuthenticator()

	// Generate key pair
	err := authenticator.GenerateKeyPair()
	require.NoError(t, err, "Key pair generation should succeed")

	// Test JWKS endpoint with GET method
	t.Run("HandleJWKS_GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
		recorder := httptest.NewRecorder()

		authenticator.HandleJWKS(recorder, req)

		// Verify response
		require.Equal(t, http.StatusOK, recorder.Code, "JWKS endpoint should return OK status")
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"),
			"Content-Type should be application/json")

		// Parse response body
		var jwksResponse map[string]interface{}
		err := json.Unmarshal(recorder.Body.Bytes(), &jwksResponse)
		require.NoError(t, err, "JWKS response should be valid JSON")

		// Verify response structure
		keys, ok := jwksResponse["keys"].([]interface{})
		require.True(t, ok, "JWKS response should have 'keys' array")
		require.Len(t, keys, 1, "There should be exactly one key in the key set")

		// Verify key properties
		key := keys[0].(map[string]interface{})
		assert.NotEmpty(t, key["kid"], "Key should have a key ID")
		assert.Equal(t, "sig", key["use"], "Key should have 'sig' usage")
	})

	// Test JWKS endpoint with non-GET method
	t.Run("HandleJWKS_NonGET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/.well-known/jwks.json", nil)
		recorder := httptest.NewRecorder()

		authenticator.HandleJWKS(recorder, req)

		assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code, "Non-GET methods should not be allowed")
	})
}

func TestJWKSClient(t *testing.T) {
	// Setup mock JWKS server
	mockJWKSHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		n := "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc" +
			"_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2Q" +
			"vzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0" +
			"fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw"
		// Mock response with a single JWK
		mockResponse := fmt.Sprintf(`{
			"keys": [
				{
					"kty": "RSA",
					"use": "sig",
					"kid": "test-key-1",
					"n": "%s",
					"e": "AQAB"
				}
			]
		}`,
			n)
		w.Write([]byte(mockResponse))
	})

	mockJWKSServer := httptest.NewServer(mockJWKSHandler)
	defer mockJWKSServer.Close()

	// Create JWKS client
	jwksClient := auth.NewJWKSClient(mockJWKSServer.URL, 1*time.Hour)

	// Test fetching keys
	t.Run("FetchKeys", func(t *testing.T) {
		ctx := context.Background()
		err := jwksClient.FetchKeys(ctx)
		require.NoError(t, err, "FetchKeys should succeed with valid JWKS endpoint")
	})

	// Test getting a key by ID
	t.Run("GetKey_ValidID", func(t *testing.T) {
		ctx := context.Background()
		key, err := jwksClient.GetKey(ctx, "test-key-1")
		require.NoError(t, err, "GetKey should succeed with valid key ID")
		require.NotNil(t, key, "Key should not be nil")

		// Verify key properties
		kid, ok := key.Get("kid")
		require.True(t, ok, "Key should have kid property")
		assert.Equal(t, "test-key-1", kid, "Key ID should match")
	})

	// Test getting a key with invalid ID
	t.Run("GetKey_InvalidID", func(t *testing.T) {
		ctx := context.Background()
		key, err := jwksClient.GetKey(ctx, "non-existent-key")
		assert.Error(t, err, "GetKey should fail with invalid key ID")
		assert.Nil(t, key, "Key should be nil when not found")
	})
}

func TestCreateAuthorizationHeader(t *testing.T) {
	authenticator := auth.NewPushNotificationAuthenticator()

	// Generate key pair
	err := authenticator.GenerateKeyPair()
	require.NoError(t, err, "Key pair generation should succeed")

	// Test creating authorization header
	payload := []byte(`{"test":"data"}`)
	header, err := authenticator.CreateAuthorizationHeader(payload)
	require.NoError(t, err, "CreateAuthorizationHeader should succeed")
	require.NotEmpty(t, header, "Authorization header should not be empty")

	// Verify header format
	assert.True(t, len(header) > 7, "Header should be longer than 'Bearer '")
	assert.Equal(t, "Bearer ", header[:7], "Header should start with 'Bearer '")
}

func TestEndToEndPushNotificationAuth(t *testing.T) {
	// This test simulates a full push notification authentication flow

	// Create authenticator for the "server" side
	serverAuth := auth.NewPushNotificationAuthenticator()
	err := serverAuth.GenerateKeyPair()
	require.NoError(t, err, "Server key generation should succeed")

	// Setup mock JWKS server
	jwksServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			serverAuth.HandleJWKS(w, r)
		}))
	defer jwksServer.Close()

	// Create authenticator for the "client" side
	clientAuth := auth.NewPushNotificationAuthenticator()
	clientAuth.SetJWKSClient(jwksServer.URL)

	// Test the flow
	t.Run("EndToEnd_ValidAuthentication", func(t *testing.T) {
		// Payload to sign
		payload := []byte(`{"message":"test-notification"}`)

		// Server signs the payload
		authHeader, err := serverAuth.CreateAuthorizationHeader(payload)
		require.NoError(t, err, "Creating auth header should succeed")

		// Create request with the signed payload
		req := httptest.NewRequest(http.MethodPost, "/notification", nil)
		req.Header.Set("Authorization", authHeader)

		// Client verifies the notification
		err = clientAuth.VerifyPushNotification(req, payload)
		assert.NoError(t, err, "Verification should succeed with valid signature")
	})

	// Test with modified payload
	t.Run("EndToEnd_ModifiedPayload", func(t *testing.T) {
		// Original payload
		originalPayload := []byte(`{"message":"original"}`)

		// Server signs the payload
		authHeader, err := serverAuth.CreateAuthorizationHeader(originalPayload)
		require.NoError(t, err, "Creating auth header should succeed")

		// Create request with the signed payload
		req := httptest.NewRequest(http.MethodPost, "/notification", nil)
		req.Header.Set("Authorization", authHeader)

		// Client verifies with modified payload
		modifiedPayload := []byte(`{"message":"modified"}`)
		err = clientAuth.VerifyPushNotification(req, modifiedPayload)
		assert.Error(t, err, "Verification should fail with modified payload")
		assert.Contains(t, err.Error(), "payload hash mismatch", "Error should indicate payload hash mismatch")
	})
}
