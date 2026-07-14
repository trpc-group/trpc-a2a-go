// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package pushauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestSignedSender_NoIdentity(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n, err := NewSignedSender()
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}
	if n.Authenticator() != nil {
		t.Error("expected no signing identity without a JWT option")
	}
	if err := n.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected unsigned delivery, got Authorization %q", gotAuth)
	}
}

func TestSignedSender_WithJWT_EndToEnd(t *testing.T) {
	n, err := NewSignedSender(WithJWT())
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}
	if n.Authenticator() == nil {
		t.Fatal("expected a signing identity with WithJWT")
	}

	// The server side would publish this JWKS; a receiver verifies against it.
	jwks := httptest.NewServer(http.HandlerFunc(n.Authenticator().HandleJWKS))
	defer jwks.Close()
	verifier := NewAuthenticator()
	verifier.SetJWKSClient(jwks.URL)

	var verifyErr error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verifyErr = verifier.VerifyPushNotification(r, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if err := n.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if verifyErr != nil {
		t.Errorf("receiver failed to verify the notifier's delivery: %v", verifyErr)
	}
}

// TestSignedSender_WithJWTKey_SharedAcrossReplicas is the multi-replica scenario:
// two notifiers (two server replicas) share one private key, so a receiver that
// fetched the JWKS from either replica verifies deliveries from both.
func TestSignedSender_WithJWTKey_SharedAcrossReplicas(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	replicaA, err := NewSignedSender(WithJWTKey(key, "shared-key"))
	if err != nil {
		t.Fatalf("NewSignedSender A: %v", err)
	}
	replicaB, err := NewSignedSender(WithJWTKey(key, "shared-key"))
	if err != nil {
		t.Fatalf("NewSignedSender B: %v", err)
	}

	// Receiver fetched the JWKS from replica A only.
	jwks := httptest.NewServer(http.HandlerFunc(replicaA.Authenticator().HandleJWKS))
	defer jwks.Close()
	verifier := NewAuthenticator()
	verifier.SetJWKSClient(jwks.URL)

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

	n, err := NewSignedSender(WithSenderOptions(push.WithRequestDecorator(func(r *http.Request) {
		r.Header.Set("X-Custom", "v1")
	})))
	if err != nil {
		t.Fatalf("NewSignedSender: %v", err)
	}
	if err := n.SendPush(context.Background(), testCfg(ts.URL), completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotHeader != "v1" {
		t.Errorf("sender option not forwarded: X-Custom = %q", gotHeader)
	}
}
