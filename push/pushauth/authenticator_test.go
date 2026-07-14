// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package pushauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticator_GenerateKeyPair(t *testing.T) {
	auth := NewAuthenticator()
	err := auth.GenerateKeyPair()

	require.NoError(t, err)
	assert.NotNil(t, auth.privateKey)
	assert.NotEmpty(t, auth.keyID)

	// Verify the key set has one key
	assert.Equal(t, 1, auth.keySet.Len())

	// Verify the key has the required attributes
	key, found := auth.keySet.Key(0)
	require.True(t, found)

	// Test algorithm setting (line 89-90)
	algVal, ok := key.Get(jwk.AlgorithmKey)
	require.True(t, ok)
	assert.EqualValues(t, "RS256", algVal)

	// Test key ID and usage
	kidVal, ok := key.Get(jwk.KeyIDKey)
	require.True(t, ok)
	assert.Equal(t, auth.keyID, kidVal)

	usageVal, ok := key.Get(jwk.KeyUsageKey)
	require.True(t, ok)
	assert.Equal(t, "sig", usageVal)
}

func TestAuthenticator_SignPayload(t *testing.T) {
	auth := NewAuthenticator()
	err := auth.GenerateKeyPair()
	require.NoError(t, err)

	// Test with valid payload
	payload := []byte(`{"test":"data"}`)
	tokenString, err := auth.SignPayload(payload)

	require.NoError(t, err)
	assert.NotEmpty(t, tokenString)

	// Parse token and verify header contains kid
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	require.NoError(t, err)
	assert.Equal(t, auth.keyID, token.Header["kid"])

	// Verify claims
	claims, ok := token.Claims.(jwt.MapClaims)
	require.True(t, ok)
	assert.Contains(t, claims, "iat")
	assert.Contains(t, claims, "request_body_sha256")

	// Test with error case - no private key
	authNoKey := NewAuthenticator()
	_, err = authNoKey.SignPayload(payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private key not initialized")
}

func TestAuthenticator_HandleJWKS(t *testing.T) {
	auth := NewAuthenticator()
	err := auth.GenerateKeyPair()
	require.NoError(t, err)

	// Test successful GET request
	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	w := httptest.NewRecorder()

	auth.HandleJWKS(w, req)

	// Verify response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Parse response body
	var jwksResp map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&jwksResp)
	require.NoError(t, err)

	// Verify keys array exists
	keys, ok := jwksResp["keys"].([]interface{})
	require.True(t, ok)
	assert.Len(t, keys, 1)

	// Test non-GET method
	req = httptest.NewRequest(http.MethodPost, "/.well-known/jwks.json", nil)
	w = httptest.NewRecorder()

	auth.HandleJWKS(w, req)

	resp = w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestJWKSClient_FetchKeys(t *testing.T) {
	// Create a mock JWKS server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"keys":[{"kty":"RSA","kid":"test-key-id","alg":"RS256","use":"sig","n":"test","e":"AQAB"}]}`)
	}))
	defer ts.Close()

	// Create JWKS client
	client := NewJWKSClient(ts.URL, 10*time.Minute)

	// Test fetch keys
	err := client.FetchKeys(context.Background())
	require.NoError(t, err)

	// Verify keys were fetched
	assert.Equal(t, 1, client.keySet.Len())

	// Test cache behavior - should not fetch again
	prevFetch := client.lastFetch
	err = client.FetchKeys(context.Background())
	require.NoError(t, err)
	assert.Equal(t, prevFetch, client.lastFetch) // Should not have refreshed

	// Test error cases

	// Invalid URL
	clientErr := NewJWKSClient("invalid-url", 10*time.Minute)
	err = clientErr.FetchKeys(context.Background())
	assert.Error(t, err)

	// Non-200 response
	tsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tsErr.Close()

	clientErrResp := NewJWKSClient(tsErr.URL, 10*time.Minute)
	err = clientErrResp.FetchKeys(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status code")

	// Invalid JSON
	tsBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"keys": not valid json}`)
	}))
	defer tsBadJSON.Close()

	clientBadJSON := NewJWKSClient(tsBadJSON.URL, 10*time.Minute)
	err = clientBadJSON.FetchKeys(context.Background())
	assert.Error(t, err)
}

func TestJWKSClient_GetKey(t *testing.T) {
	// Create a mock JWKS server with key
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"keys":[{"kty":"RSA","kid":"test-key-id","alg":"RS256","use":"sig","n":"test","e":"AQAB"}]}`)
	}))
	defer ts.Close()

	// Create JWKS client
	client := NewJWKSClient(ts.URL, 10*time.Minute)

	// Get key by ID
	key, err := client.GetKey(context.Background(), "test-key-id")
	require.NoError(t, err)
	assert.NotNil(t, key)

	// Get non-existent key
	_, err = client.GetKey(context.Background(), "non-existent-key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAuthenticator_CreateAuthorizationHeader(t *testing.T) {
	auth := NewAuthenticator()
	err := auth.GenerateKeyPair()
	require.NoError(t, err)

	payload := []byte(`{"test":"data"}`)
	header, err := auth.CreateAuthorizationHeader(payload)

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(header, "Bearer "))

	// Test error case
	authNoKey := NewAuthenticator()
	_, err = authNoKey.CreateAuthorizationHeader(payload)
	assert.Error(t, err)
}

func TestAuthenticator_SetJWKSClient(t *testing.T) {
	auth := NewAuthenticator()

	// Initially jwksClient should be nil
	assert.Nil(t, auth.jwksClient)

	// Set JWKS client
	auth.SetJWKSClient("http://example.com/jwks.json")

	// Verify client was set
	assert.NotNil(t, auth.jwksClient)
	assert.Equal(t, "http://example.com/jwks.json", auth.jwksClient.jwksURL)
}

func TestAuthenticator_VerifyPushNotification(t *testing.T) {
	// Agent side: generate a key pair and publish it via a JWKS endpoint.
	signer := NewAuthenticator()
	require.NoError(t, signer.GenerateKeyPair())

	jwksServer := httptest.NewServer(http.HandlerFunc(signer.HandleJWKS))
	defer jwksServer.Close()

	// Client side: verify tokens against that JWKS endpoint.
	verifier := NewAuthenticator()
	verifier.SetJWKSClient(jwksServer.URL)

	payload := []byte(`{"task_id":"task-1","status":"completed"}`)

	// newRequest builds a POST carrying the given Authorization header and body.
	newRequest := func(authHeader string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/push", strings.NewReader(string(payload)))
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		return req
	}

	t.Run("valid token", func(t *testing.T) {
		header, err := signer.CreateAuthorizationHeader(payload)
		require.NoError(t, err)
		require.NoError(t, verifier.VerifyPushNotification(newRequest(header), payload))
	})

	t.Run("missing authorization header", func(t *testing.T) {
		err := verifier.VerifyPushNotification(newRequest(""), payload)
		require.ErrorIs(t, err, ErrMissingToken)
	})

	t.Run("malformed authorization header", func(t *testing.T) {
		// A bare token without the "Bearer " prefix must be rejected.
		token, err := signer.SignPayload(payload)
		require.NoError(t, err)
		err = verifier.VerifyPushNotification(newRequest(token), payload)
		require.ErrorIs(t, err, ErrInvalidAuthHeader)
	})

	t.Run("payload hash mismatch", func(t *testing.T) {
		header, err := signer.CreateAuthorizationHeader(payload)
		require.NoError(t, err)
		// Verify against a body different from the one that was signed.
		tampered := []byte(`{"task_id":"task-1","status":"tampered"}`)
		err = verifier.VerifyPushNotification(newRequest(header), tampered)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "payload hash mismatch")
	})

	t.Run("expired token", func(t *testing.T) {
		// Sign a token whose iat/exp are in the past, reusing the signer's key.
		hash := fmt.Sprintf("%x", sha256.Sum256(payload))
		stale := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iat":                 time.Now().Add(-2 * time.Hour).Unix(),
			"exp":                 time.Now().Add(-1 * time.Hour).Unix(),
			"request_body_sha256": hash,
		})
		stale.Header["kid"] = signer.keyID
		signed, err := stale.SignedString(signer.privateKey)
		require.NoError(t, err)
		err = verifier.VerifyPushNotification(newRequest("Bearer "+signed), payload)
		require.Error(t, err)
	})

	t.Run("unknown key id", func(t *testing.T) {
		// A signer whose key is not published on the verifier's JWKS endpoint.
		other := NewAuthenticator()
		require.NoError(t, other.GenerateKeyPair())
		header, err := other.CreateAuthorizationHeader(payload)
		require.NoError(t, err)
		err = verifier.VerifyPushNotification(newRequest(header), payload)
		require.Error(t, err)
	})

	t.Run("jwks client not initialized", func(t *testing.T) {
		bare := NewAuthenticator()
		header, err := signer.CreateAuthorizationHeader(payload)
		require.NoError(t, err)
		err = bare.VerifyPushNotification(newRequest(header), payload)
		require.Error(t, err)
	})
}
