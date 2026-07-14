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
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lestrrat-go/jwx/v2/jwk"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

func testCfg(url string) protocol.TaskPushNotificationConfig {
	return protocol.TaskPushNotificationConfig{TaskID: "t1", URL: url}
}

func completedEvent(taskID string) protocol.StreamResponse {
	return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
		Final:  true,
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
	})
}

func newTestSignedSender(opts ...SignedSenderOption) (*SignedSender, error) {
	opts = append(opts, WithSenderOptions(push.WithUnsafeAllowPrivateNetworks()))
	return NewSignedSender(opts...)
}

func TestSignedSender_DefaultIdentity_EndToEnd(t *testing.T) {
	s, err := newTestSignedSender()
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}

	// The server side would publish this JWKS; a receiver verifies against it.
	jwks := httptest.NewServer(s.JWKSHandler())
	defer jwks.Close()
	verifier := NewVerifier(jwks.URL)

	var verifyErr error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifyErr = verifier.VerifyPushNotification(r, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if err := s.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if verifyErr != nil {
		t.Errorf("receiver failed to verify the sender's delivery: %v", verifyErr)
	}
}

// TestSignedSender_WithJWTKey_SharedAcrossReplicas is the multi-replica scenario:
// two senders (two server replicas) share one private key, so a receiver that
// fetched the JWKS from either replica verifies deliveries from both.
func TestSignedSender_WithJWTKey_SharedAcrossReplicas(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	replicaA, err := newTestSignedSender(WithJWTKey(key, ""))
	if err != nil {
		t.Fatalf("NewSignedSender A: %v", err)
	}
	replicaB, err := newTestSignedSender(WithJWTKey(key, ""))
	if err != nil {
		t.Fatalf("NewSignedSender B: %v", err)
	}

	publicJWK, err := jwk.FromRaw(key.Public())
	if err != nil {
		t.Fatalf("create public JWK: %v", err)
	}
	thumbprint, err := publicJWK.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatalf("derive thumbprint: %v", err)
	}
	wantKeyID := base64.RawURLEncoding.EncodeToString(thumbprint)
	if replicaA.signer.keyID != wantKeyID || replicaB.signer.keyID != wantKeyID {
		t.Fatalf("key ID must be stable across replicas: A=%q B=%q want=%q",
			replicaA.signer.keyID, replicaB.signer.keyID, wantKeyID)
	}

	// Receiver fetched the JWKS from replica A only.
	jwks := httptest.NewServer(replicaA.JWKSHandler())
	defer jwks.Close()
	verifier := NewVerifier(jwks.URL)

	var verifyErr error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifyErr = verifier.VerifyPushNotification(r, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// The delivery comes from replica B.
	if err := replicaB.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush from replica B: %v", err)
	}
	if verifyErr != nil {
		t.Errorf("replica A's JWKS failed to verify replica B's delivery: %v", verifyErr)
	}
}

func TestSignedSender_WithJWTKey_RequiresKey(t *testing.T) {
	if _, err := NewSignedSender(WithJWTKey(nil, "kid")); err == nil {
		t.Error("expected an error for a nil private key")
	}
}

func TestSignedSender_WithSenderOptions(t *testing.T) {
	var gotHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s, err := newTestSignedSender(WithSenderOptions(push.WithRequestDecorator(func(
		_ context.Context, _ protocol.TaskPushNotificationConfig, r *http.Request,
	) error {
		r.Header.Set("X-Custom", "v1")
		return nil
	})))
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}
	if err := s.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotHeader != "v1" {
		t.Errorf("sender option not forwarded: X-Custom = %q", gotHeader)
	}
}

func TestSignedSender_SigningHookCannotBeOverridden(t *testing.T) {
	s, err := newTestSignedSender(WithSenderOptions(push.WithAuthorizationHeader(
		func(context.Context, protocol.TaskPushNotificationConfig, []byte) (string, error) {
			return "Bearer override", nil
		},
	)))
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}

	jwks := httptest.NewServer(s.JWKSHandler())
	defer jwks.Close()
	verifier := NewVerifier(jwks.URL)

	var verifyErr error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifyErr = verifier.VerifyPushNotification(r, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if err := s.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if verifyErr != nil {
		t.Errorf("forwarded WithAuthorizationHeader replaced the signing hook: %v", verifyErr)
	}
}

func TestSignedSender_ClientAuthenticationTakesPriority(t *testing.T) {
	var gotAuthorization string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s, err := newTestSignedSender()
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}
	cfg := testCfg(ts.URL)
	cfg.Authentication = &protocol.AuthenticationInfo{
		Scheme:      "Basic",
		Credentials: "client-credential",
	}
	if err := s.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotAuthorization != "Basic client-credential" {
		t.Errorf("Authorization = %q, want client-declared authentication", gotAuthorization)
	}
}
