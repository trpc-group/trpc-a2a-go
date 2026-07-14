// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func completedEvent(taskID string) protocol.StreamResponse {
	return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
		Final:  true,
		Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
	})
}

func newTestHTTPSender(opts ...SenderOption) *HTTPSender {
	opts = append(opts, WithUnsafeAllowPrivateNetworks())
	return NewHTTPSender(opts...)
}

func TestHTTPSender_SendPush_DeliversStreamResponse(t *testing.T) {
	var gotBody []byte
	var gotToken, gotAuth, gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotToken = r.Header.Get(notificationTokenHeader)
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := newTestHTTPSender()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: ts.URL, Token: "tok-1"}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}

	if gotCT != "application/a2a+json" {
		t.Errorf("Content-Type = %q, want application/a2a+json", gotCT)
	}
	if gotToken != "tok-1" {
		t.Errorf("notification token header = %q, want tok-1", gotToken)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization without signer/auth, got %q", gotAuth)
	}
	// The body must be a spec-shaped StreamResponse carrying the statusUpdate.
	var rr protocol.StreamResponse
	if err := json.Unmarshal(gotBody, &rr); err != nil {
		t.Fatalf("body is not a StreamResponse: %v; body=%s", err, gotBody)
	}
	if su := rr.GetStatusUpdate(); su == nil || su.Status.State != protocol.TaskStateCompleted {
		t.Errorf("expected a completed statusUpdate, got %s", gotBody)
	}
}

func TestHTTPSender_SendPush_StaticAuth(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := newTestHTTPSender()
	cfg := protocol.TaskPushNotificationConfig{
		TaskID:         "t1",
		URL:            ts.URL,
		Authentication: &protocol.AuthenticationInfo{Scheme: "Bearer", Credentials: "abc"},
	}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotAuth != "Bearer abc" {
		t.Errorf("Authorization = %q, want 'Bearer abc'", gotAuth)
	}
}

func TestHTTPSender_SendPush_AuthorizationHeaderHook(t *testing.T) {
	// The signing seam: WithAuthorizationHeader sets the Authorization header and
	// receives the exact marshaled body. (End-to-end JWT signing + JWKS
	// verification is covered in push/pushauth.)
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var gotPayload []byte
	sender := newTestHTTPSender(WithAuthorizationHeader(
		func(_ context.Context, cfg protocol.TaskPushNotificationConfig, payload []byte) (string, error) {
			if cfg.TaskID != "t1" {
				t.Fatalf("hook config task ID = %q", cfg.TaskID)
			}
			gotPayload = payload
			return "Bearer signed-xyz", nil
		}))
	cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: ts.URL}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotAuth != "Bearer signed-xyz" {
		t.Errorf("Authorization = %q, want the hook's value", gotAuth)
	}
	if len(gotPayload) == 0 {
		t.Error("the hook did not receive the marshaled body")
	}
}

func TestHTTPSender_SendPush_ErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	sender := newTestHTTPSender()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: ts.URL}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err == nil {
		t.Error("expected an error for a non-2xx response")
	}
}

func TestHTTPSender_SendPush_EmptyURL(t *testing.T) {
	sender := newTestHTTPSender()
	err := sender.SendPush(context.Background(),
		protocol.TaskPushNotificationConfig{TaskID: "t1"}, completedEvent("t1"))
	if err == nil {
		t.Error("expected an error for an empty URL")
	}
}

func TestHTTPSender_SendPush_RequestDecorator(t *testing.T) {
	var gotCustom, gotContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Custom-Header")
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := newTestHTTPSender(WithRequestDecorator(func(
		_ context.Context, _ protocol.TaskPushNotificationConfig, r *http.Request,
	) error {
		r.Header.Set("X-Custom-Header", "custom-value")
		return nil
	}))
	cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: ts.URL}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotCustom != "custom-value" {
		t.Errorf("decorator header = %q, want custom-value", gotCustom)
	}
	// Decorators run after the sender's own headers, which remain set.
	if gotContentType != "application/a2a+json" {
		t.Errorf("Content-Type = %q, want application/a2a+json", gotContentType)
	}
}

func TestHTTPSender_SendPush_ConfigAuthWinsOverSigner(t *testing.T) {
	// A signing hook is configured, but the config declares its own credential —
	// the client-registered credential must win (the client states how the agent
	// authenticates to its webhook).
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := newTestHTTPSender(WithAuthorizationHeader(
		func(context.Context, protocol.TaskPushNotificationConfig, []byte) (string, error) {
			return "Bearer JWT-should-not-win", nil
		}))
	cfg := protocol.TaskPushNotificationConfig{
		TaskID:         "t1",
		URL:            ts.URL,
		Authentication: &protocol.AuthenticationInfo{Scheme: "Basic", Credentials: "client-cred"},
	}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotAuth != "Basic client-cred" {
		t.Errorf("Authorization = %q, want the client-registered credential to win", gotAuth)
	}
}

func TestHTTPSender_SendPush_RejectsBadURL(t *testing.T) {
	sender := newTestHTTPSender()
	for _, tc := range []struct{ name, url string }{
		{"non-http scheme", "ftp://example.com/hook"},
		{"file scheme", "file:///etc/passwd"},
		{"no host", "https:///path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: tc.url}
			if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err == nil {
				t.Errorf("expected an error for URL %q", tc.url)
			}
		})
	}
}

func TestHTTPSender_SendPush_URLPolicyRejects(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := newTestHTTPSender(WithURLPolicy(func(*url.URL) error { return errors.New("host not allowed") }))
	cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: ts.URL}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err == nil {
		t.Error("expected the URL policy to reject the delivery")
	}
}

func TestHTTPSender_SendPush_NoRedirect(t *testing.T) {
	// A webhook that redirects must not be followed (SSRF vector); the 3xx is a
	// failed delivery.
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	sender := newTestHTTPSender()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "t1", URL: redirector.URL}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err == nil {
		t.Error("expected a redirect to be treated as a failed delivery")
	}
}

func TestHTTPSender_SendPush_RejectsPrivateNetworkByDefault(t *testing.T) {
	var called bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := NewHTTPSender()
	err := sender.SendPush(context.Background(),
		protocol.TaskPushNotificationConfig{TaskID: "t1", URL: ts.URL}, completedEvent("t1"))
	if err == nil {
		t.Fatal("expected the default sender to reject a loopback webhook")
	}
	if called {
		t.Fatal("private webhook was contacted before rejection")
	}
}

func TestHTTPSender_SendPush_ResolvesDeclaredAuthentication(t *testing.T) {
	var gotAuthorization string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	sender := newTestHTTPSender(WithAuthorizationHeader(func(
		_ context.Context, cfg protocol.TaskPushNotificationConfig, _ []byte,
	) (string, error) {
		if cfg.Authentication == nil || cfg.Authentication.Scheme != "Bearer" {
			t.Fatalf("resolver did not receive declared authentication: %+v", cfg.Authentication)
		}
		return "Bearer dynamic-token", nil
	}))
	cfg := protocol.TaskPushNotificationConfig{
		TaskID: "t1",
		URL:    ts.URL,
		Authentication: &protocol.AuthenticationInfo{
			Scheme: "Bearer",
		},
	}
	if err := sender.SendPush(context.Background(), cfg, completedEvent("t1")); err != nil {
		t.Fatalf("SendPush: %v", err)
	}
	if gotAuthorization != "Bearer dynamic-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
}

func TestHTTPSender_SendPush_RejectsAuthenticationWithoutScheme(t *testing.T) {
	sender := newTestHTTPSender()
	err := sender.SendPush(context.Background(), protocol.TaskPushNotificationConfig{
		TaskID: "t1",
		URL:    "https://example.com/hook",
		Authentication: &protocol.AuthenticationInfo{
			Credentials: "credential",
		},
	}, completedEvent("t1"))
	if err == nil {
		t.Fatal("expected missing authentication scheme to be rejected")
	}
}
