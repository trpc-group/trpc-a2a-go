// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package pushauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

// bearerScheme is the Authorization scheme used for push-notification JWTs.
const bearerScheme = "Bearer"

// pushTokenTTL bounds how long a signed push-notification token stays valid.
// It is used both to set the token's "exp" claim on the signing side and to
// bound the "iat" age on the verifying side.
const pushTokenTTL = 5 * time.Minute

// Push-notification verification errors.
var (
	ErrMissingToken      = errors.New("missing authentication token")
	ErrInvalidAuthHeader = errors.New("invalid authorization header format")
	ErrInvalidToken      = errors.New("invalid authentication token")
	ErrTokenExpired      = errors.New("token has expired")
)

// Authenticator is the trust layer for push notifications. It plays both sides:
//
//   - Agent (signing): GenerateKeyPair creates an RSA key pair, SignPayload /
//     CreateAuthorizationHeader sign the notification body into a JWT, and
//     HandleJWKS publishes the public keys as a JWKS endpoint.
//   - Client (verifying): SetJWKSClient points at the agent's JWKS endpoint and
//     VerifyPushNotification checks a received notification's signature, payload
//     hash, and freshness.
//
// It does not deliver notifications; use SignedSender for agent-side delivery
// with JWT signing, or HTTPSender for transport without a signing identity.
type Authenticator struct {
	// For sending notifications (agent side).
	privateKey *rsa.PrivateKey
	keySet     jwk.Set
	keyID      string

	// For verifying notifications (client side).
	jwksClient *JWKSClient
}

// NewAuthenticator creates a new push notification authenticator.
func NewAuthenticator() *Authenticator {
	return &Authenticator{
		keySet: jwk.NewSet(),
	}
}

// GenerateKeyPair generates a new RSA key pair for signing push notifications.
func (a *Authenticator) GenerateKeyPair() error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate RSA key: %w", err)
	}
	return a.UseKeyPair(privateKey, "")
}

// UseKeyPair installs an existing RSA private key for signing push notifications
// and publishes its public half in the JWKS key set. Use it instead of
// GenerateKeyPair when the key must survive restarts or be shared across
// replicas (so every instance signs with the same key the JWKS advertises).
// When keyID is empty, a stable ID is derived from the public key according to
// RFC 7638. Replicas using the same key therefore publish and sign with the same
// key ID.
func (a *Authenticator) UseKeyPair(privateKey *rsa.PrivateKey, keyID string) error {
	if privateKey == nil {
		return errors.New("private key is required")
	}
	// Create a JWK from the private key
	key, err := jwk.FromRaw(privateKey.Public())
	if err != nil {
		return fmt.Errorf("failed to create JWK from public key: %w", err)
	}
	if keyID == "" {
		thumbprint, err := key.Thumbprint(crypto.SHA256)
		if err != nil {
			return fmt.Errorf("failed to derive key ID: %w", err)
		}
		keyID = base64.RawURLEncoding.EncodeToString(thumbprint)
	}

	// Set key ID
	if err := key.Set(jwk.KeyIDKey, keyID); err != nil {
		return fmt.Errorf("failed to set key ID: %w", err)
	}

	// Set key usage
	if err := key.Set(jwk.KeyUsageKey, "sig"); err != nil {
		return fmt.Errorf("failed to set key usage: %w", err)
	}

	// Set algorithm
	if err := key.Set(jwk.AlgorithmKey, "RS256"); err != nil {
		return fmt.Errorf("failed to set key algorithm: %w", err)
	}

	// Add the key to the key set
	a.keySet.AddKey(key)

	a.privateKey = privateKey
	a.keyID = keyID
	return nil
}

// SignPayload signs a payload for push notification.
func (a *Authenticator) SignPayload(payload []byte) (string, error) {
	if a.privateKey == nil {
		return "", errors.New("private key not initialized")
	}
	// Calculate SHA256 hash of payload.
	hash := sha256.Sum256(payload)
	payloadHash := fmt.Sprintf("%x", hash)
	// Create token with claims. "exp" gives the token an explicit expiry that
	// the standard JWT validator enforces on the receiving side, so a captured
	// token cannot be replayed indefinitely.
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat":                 now.Unix(),
		"exp":                 now.Add(pushTokenTTL).Unix(),
		"request_body_sha256": payloadHash,
	})
	// Set key ID in token header.
	token.Header["kid"] = a.keyID
	// Sign the token.
	tokenString, err := token.SignedString(a.privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}
	return tokenString, nil
}

// HandleJWKS handles requests to the JWKS endpoint.
func (a *Authenticator) HandleJWKS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Marshal the entire key set to JSON
	keySetJSON, err := json.Marshal(a.keySet)
	if err != nil {
		http.Error(w, "Failed to marshal key set", http.StatusInternalServerError)
		return
	}

	// Parse the key set JSON to extract keys properly
	var keySetMap map[string]interface{}
	if err := json.Unmarshal(keySetJSON, &keySetMap); err != nil {
		http.Error(w, "Failed to process key set", http.StatusInternalServerError)
		return
	}

	// Construct the proper response format
	response := map[string]interface{}{
		"keys": keySetMap["keys"],
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode JWKS", http.StatusInternalServerError)
	}
}

// JWKSClient retrieves and caches JWKs from a remote endpoint.
type JWKSClient struct {
	jwksURL string
	// mu guards keySet and lastFetch: a JWKSClient is shared across concurrent
	// push-notification verifications, each of which may trigger a refresh.
	mu        sync.RWMutex
	keySet    jwk.Set
	lastFetch time.Time
	cacheTTL  time.Duration
}

// NewJWKSClient creates a new JWKS client for a specific URL.
func NewJWKSClient(jwksURL string, cacheTTL time.Duration) *JWKSClient {
	if cacheTTL == 0 {
		cacheTTL = 1 * time.Hour
	}
	return &JWKSClient{
		jwksURL:  jwksURL,
		keySet:   jwk.NewSet(),
		cacheTTL: cacheTTL,
	}
}

// FetchKeys fetches the JWKs from the remote endpoint.
func (c *JWKSClient) FetchKeys(ctx context.Context) error {
	// Check if we need to refresh the keys. Read the cache state under the lock
	// so we never race a concurrent writer swapping the key set below.
	c.mu.RLock()
	fresh := !c.lastFetch.IsZero() && time.Since(c.lastFetch) < c.cacheTTL
	c.mu.RUnlock()
	if fresh {
		return nil
	}
	// Fetch the JWKs from the remote endpoint (no lock held during I/O).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}
	var jwksResponse struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwksResponse); err != nil {
		return fmt.Errorf("failed to unmarshal JWKS response: %w", err)
	}
	// Create a new key set.
	newKeySet := jwk.NewSet()
	for _, rawKey := range jwksResponse.Keys {
		key, err := jwk.ParseKey(rawKey)
		if err != nil {
			return fmt.Errorf("failed to parse JWK: %w", err)
		}
		newKeySet.AddKey(key)
	}
	c.mu.Lock()
	c.keySet = newKeySet
	c.lastFetch = time.Now()
	c.mu.Unlock()
	return nil
}

// GetKey returns a key with the specified ID.
func (c *JWKSClient) GetKey(ctx context.Context, keyID string) (jwk.Key, error) {
	if err := c.FetchKeys(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, found := c.keySet.LookupKeyID(keyID)
	if !found {
		return nil, fmt.Errorf("key with ID %s not found", keyID)
	}
	return key, nil
}

// VerifyPushNotification verifies a push notification JWT and payload.
func (a *Authenticator) VerifyPushNotification(r *http.Request, payload []byte) error {
	// Initialize the JWKS client if needed.
	if a.jwksClient == nil {
		return errors.New("JWKS client not initialized")
	}
	// Extract the JWT from the Authorization header.
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ErrMissingToken
	}
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], bearerScheme) {
		return ErrInvalidAuthHeader
	}
	tokenString := parts[1]
	// Parse the JWT without verifying to extract the key ID.
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return fmt.Errorf("failed to parse token: %w", err)
	}
	// Extract the key ID from the token header.
	keyID, ok := token.Header["kid"].(string)
	if !ok {
		return errors.New("token missing key ID")
	}
	// Get the public key from the JWKS.
	key, err := a.jwksClient.GetKey(r.Context(), keyID)
	if err != nil {
		return fmt.Errorf("failed to get key: %w", err)
	}
	// Extract the public key.
	var publicKey interface{}
	if err := key.Raw(&publicKey); err != nil {
		return fmt.Errorf("failed to extract public key: %w", err)
	}
	// Parse and validate the token.
	claims := jwt.MapClaims{}
	parsedToken, err := jwt.ParseWithClaims(
		tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return publicKey, nil
		})
	if err != nil {
		return fmt.Errorf("failed to validate token: %w", err)
	}
	if !parsedToken.Valid {
		return ErrInvalidToken
	}
	// Verify the payload hash.
	hash := sha256.Sum256(payload)
	payloadHash := fmt.Sprintf("%x", hash)
	if claimHash, ok := claims["request_body_sha256"].(string); !ok || claimHash != payloadHash {
		return errors.New("payload hash mismatch")
	}
	// Verify the token age.
	if iat, ok := claims["iat"].(float64); ok {
		tokenAge := time.Since(time.Unix(int64(iat), 0))
		if tokenAge > pushTokenTTL {
			return ErrTokenExpired
		}
	} else {
		return errors.New("token missing issued at time")
	}
	return nil
}

// SetJWKSClient sets the JWKS client for verifying push notifications.
func (a *Authenticator) SetJWKSClient(jwksURL string) {
	a.jwksClient = NewJWKSClient(jwksURL, 1*time.Hour)
}

// CreateAuthorizationHeader creates an Authorization header for push notifications.
func (a *Authenticator) CreateAuthorizationHeader(payload []byte) (string, error) {
	token, err := a.SignPayload(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s", bearerScheme, token), nil
}
