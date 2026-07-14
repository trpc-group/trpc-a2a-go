// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// discoveryProcessor is a minimal MessageProcessor for wiring a real memory
// TaskManager into discovery tests.
type discoveryProcessor struct{}

func (discoveryProcessor) ProcessMessage(
	ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	out := make(chan protocol.StreamEvent)
	close(out)
	return out, nil
}

func discoveryCard() AgentCard {
	return AgentCard{
		Name:    "Discovery Test Agent",
		URL:     "http://localhost:8080/",
		Version: "1.0.0",
	}
}

// newDiscoveryTM builds a memory TaskManager with the given push options.
func newDiscoveryTM(t *testing.T, opts ...memory.TaskManagerOption) *memory.TaskManager {
	t.Helper()
	tm, err := memory.NewTaskManager(discoveryProcessor{}, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { tm.Close() })
	return tm
}

func fetchDefaultCard(t *testing.T, ts *httptest.Server) AgentCard {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + protocol.AgentCardPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var card AgentCard
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&card))
	return card
}

// TestNewA2AServer_PublishesConfiguredJWKS: with a JWKS handler passed via
// WithPushNotificationJWKSHandler, the server publishes the keys, and it
// advertises the push capability automatically from the TaskManager's Sender.
func TestNewA2AServer_PublishesConfiguredJWKS(t *testing.T) {
	sender, err := pushauth.NewSignedSender()
	require.NoError(t, err)
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()),
		WithPushNotificationJWKSHandler(sender.JWKSHandler()))
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// JWKS is published for the configured identity.
	resp, err := ts.Client().Get(ts.URL + protocol.JWKSPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "JWKS should be published")

	// The served card advertises the push capability (from the Sender).
	card := fetchDefaultCard(t, ts)
	require.NotNil(t, card.Capabilities.PushNotifications)
	assert.True(t, *card.Capabilities.PushNotifications, "capability should be auto-set")
}

// TestNewA2AServer_PushWithoutJWKS: a plain Sender enables push (capability
// auto-set) but publishes no JWKS, and construction succeeds.
func TestNewA2AServer_PushWithoutJWKS(t *testing.T) {
	sender := push.NewHTTPSender()
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()))
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + protocol.JWKSPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	// The unregistered path falls through to the JSON-RPC root route (400).
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "no JWKS without an identity")

	card := fetchDefaultCard(t, ts)
	require.NotNil(t, card.Capabilities.PushNotifications)
	assert.True(t, *card.Capabilities.PushNotifications)
}

// TestNewA2AServer_JWKSExplicitDisable: WithJWKSEndpoint(false, "") is the
// sanctioned escape hatch — an identity is configured but the endpoint stays
// off, e.g. when a gateway serves the keys.
func TestNewA2AServer_JWKSExplicitDisable(t *testing.T) {
	sender, err := pushauth.NewSignedSender()
	require.NoError(t, err)
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()),
		WithPushNotificationJWKSHandler(sender.JWKSHandler()),
		WithJWKSEndpoint(false, ""))
	require.NoError(t, err, "explicit disable must not conflict with a configured identity")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + protocol.JWKSPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	// The unregistered path falls through to the JSON-RPC root route (400).
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "explicit disable wins over discovery")
}

// TestNewA2AServer_CapabilityContract prevents unsupported advertisement while
// allowing a card to disable a capability supported by the shared manager.
func TestNewA2AServer_CapabilityContract(t *testing.T) {
	sender, err := pushauth.NewSignedSender()
	require.NoError(t, err)
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	t.Run("explicit false disables one card", func(t *testing.T) {
		card := discoveryCard()
		f := false
		card.Capabilities.PushNotifications = &f
		srv, err := NewA2AServer(tm, WithAgentCard(card))
		require.NoError(t, err)
		resolved, ok := srv.resolveAgentCard(context.Background(), "")
		require.True(t, ok)
		require.NotNil(t, resolved.Capabilities.PushNotifications)
		assert.False(t, *resolved.Capabilities.PushNotifications)
	})

	t.Run("signed card remains immutable", func(t *testing.T) {
		card := discoveryCard()
		card.Signatures = []protocol.AgentCardSignature{{Protected: "hdr", Signature: "sig"}}
		srv, err := NewA2AServer(tm, WithAgentCard(card))
		require.NoError(t, err)
		resolved, ok := srv.resolveAgentCard(context.Background(), "")
		require.True(t, ok)
		assert.Nil(t, resolved.Capabilities.PushNotifications)
	})

	t.Run("explicit true without manager support", func(t *testing.T) {
		card := discoveryCard()
		v := true
		card.Capabilities.PushNotifications = &v
		_, err := NewA2AServer(newDiscoveryTM(t), WithAgentCard(card))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not support push")
	})
}

// TestNewA2AServer_JWKSWithoutSenderNoCapability: a JWKS handler does
// not by itself advertise the push capability — that requires the TaskManager's
// Sender. Without one, the card must not claim pushNotifications, and no JWKS is
// published (which would otherwise contradict config RPCs returning -32003).
func TestNewA2AServer_JWKSWithoutSenderNoCapability(t *testing.T) {
	sender, err := pushauth.NewSignedSender()
	require.NoError(t, err)
	tm := newDiscoveryTM(t) // NO push sender wired.

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()),
		WithPushNotificationJWKSHandler(sender.JWKSHandler()))
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Push capability must not be advertised (nil or explicit false).
	card := fetchDefaultCard(t, ts)
	if card.Capabilities.PushNotifications != nil {
		assert.False(t, *card.Capabilities.PushNotifications,
			"a signing identity alone must not advertise push capability without a Sender")
	}

	// And no JWKS is published without a delivery capability.
	resp, err := ts.Client().Get(ts.URL + protocol.JWKSPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "no JWKS without a delivery capability")
}
