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

// JWTSigner owns an agent-side signing identity and publishes its public
// keys. Most agents should use SignedSender, which initializes this identity by
// construction. Receivers should use Verifier.
type JWTSigner struct {
	privateKey *rsa.PrivateKey
	keySet     jwk.Set
	keyID      string
}

// NewJWTSigner creates an uninitialized push-notification JWT signer. Call
// GenerateKeyPair or UseKeyPair before signing.
func NewJWTSigner() *JWTSigner {
	return &JWTSigner{
		keySet: jwk.NewSet(),
	}
}

// GenerateKeyPair generates a new RSA key pair for signing push notifications.
func (a *JWTSigner) GenerateKeyPair() error {
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
func (a *JWTSigner) UseKeyPair(privateKey *rsa.PrivateKey, keyID string) error {
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
func (a *JWTSigner) SignPayload(payload []byte) (string, error) {
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
func (a *JWTSigner) HandleJWKS(w http.ResponseWriter, r *http.Request) {
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
	mu         sync.RWMutex
	keySet     jwk.Set
	lastFetch  time.Time
	cacheTTL   time.Duration
	httpClient *http.Client
	// fetchMu serializes network refreshes. Key readers keep using the previous
	// immutable set while a refresh is in flight.
	fetchMu sync.Mutex
}

const maxJWKSResponseSize = 1 << 20

// NewJWKSClient creates a new JWKS client for a specific URL.
func NewJWKSClient(jwksURL string, cacheTTL time.Duration) *JWKSClient {
	return newJWKSClient(jwksURL, cacheTTL, nil)
}

func newJWKSClient(jwksURL string, cacheTTL time.Duration, httpClient *http.Client) *JWKSClient {
	if cacheTTL == 0 {
		cacheTTL = 1 * time.Hour
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	} else {
		clone := *httpClient
		if clone.Timeout <= 0 || clone.Timeout > 10*time.Second {
			clone.Timeout = 10 * time.Second
		}
		httpClient = &clone
	}
	return &JWKSClient{
		jwksURL:    jwksURL,
		keySet:     jwk.NewSet(),
		cacheTTL:   cacheTTL,
		httpClient: httpClient,
	}
}

// FetchKeys fetches the JWKs from the remote endpoint.
func (c *JWKSClient) FetchKeys(ctx context.Context) error {
	if c.keysFresh() {
		return nil
	}
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	if c.keysFresh() {
		return nil
	}
	return c.fetchKeys(ctx)
}

func (c *JWKSClient) keysFresh() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.lastFetch.IsZero() && time.Since(c.lastFetch) < c.cacheTTL
}

func (c *JWKSClient) fetchKeys(ctx context.Context) error {
	// Fetch the JWKs from the remote endpoint (no lock held during I/O).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseSize+1))
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}
	if len(body) > maxJWKSResponseSize {
		return fmt.Errorf("JWKS response exceeds %d bytes", maxJWKSResponseSize)
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
	key, found, observedFetch := c.lookupKey(keyID)
	if found {
		return key, nil
	}

	// An unknown kid is the normal signal for key rotation. Bypass the TTL once;
	// concurrent misses that observed the same generation share this refresh.
	c.fetchMu.Lock()
	c.mu.RLock()
	alreadyRefreshed := c.lastFetch.After(observedFetch)
	c.mu.RUnlock()
	if !alreadyRefreshed {
		if err := c.fetchKeys(ctx); err != nil {
			c.fetchMu.Unlock()
			return nil, err
		}
	}
	c.fetchMu.Unlock()

	key, found, _ = c.lookupKey(keyID)
	if !found {
		return nil, fmt.Errorf("key with ID %s not found", keyID)
	}
	return key, nil
}

func (c *JWKSClient) lookupKey(keyID string) (jwk.Key, bool, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, found := c.keySet.LookupKeyID(keyID)
	return key, found, c.lastFetch
}

// VerifierConfig configures receiver-side JWKS caching and HTTP access.
type VerifierConfig struct {
	CacheTTL   time.Duration
	HTTPClient *http.Client
}

// Verifier validates signed push notifications against a remote JWKS.
type Verifier struct {
	jwksClient *JWKSClient
}

// NewVerifier creates a ready-to-use receiver. An optional config customizes
// the one-hour cache and the HTTP client used to retrieve JWKS.
func NewVerifier(jwksURL string, configs ...VerifierConfig) *Verifier {
	var cfg VerifierConfig
	if len(configs) > 0 {
		cfg = configs[0]
	}
	return &Verifier{jwksClient: newJWKSClient(jwksURL, cfg.CacheTTL, cfg.HTTPClient)}
}

// VerifyPushNotification verifies a push notification JWT and payload.
func (v *Verifier) VerifyPushNotification(r *http.Request, payload []byte) error {
	if v == nil || v.jwksClient == nil {
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
	key, err := v.jwksClient.GetKey(r.Context(), keyID)
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
		}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}))
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

// CreateAuthorizationHeader creates an Authorization header for push notifications.
func (a *JWTSigner) CreateAuthorizationHeader(payload []byte) (string, error) {
	token, err := a.SignPayload(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s", bearerScheme, token), nil
}
