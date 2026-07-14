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

// TestNewA2AServer_PublishesConfiguredIdentity: with a signing identity passed
// via WithPushNotificationAuthenticator, the server publishes the JWKS, and it
// advertises the push capability automatically from the TaskManager's Sender.
func TestNewA2AServer_PublishesConfiguredIdentity(t *testing.T) {
	sender, err := pushauth.NewSignedSender(pushauth.WithJWT())
	require.NoError(t, err)
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()),
		WithPushNotificationAuthenticator(sender.Authenticator()))
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

// TestNewA2AServer_UnsignedPush: an identity-less Sender enables push (capability
// auto-set) but publishes no JWKS, and construction succeeds.
func TestNewA2AServer_UnsignedPush(t *testing.T) {
	sender, err := pushauth.NewSignedSender() // no signing identity
	require.NoError(t, err)
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
	sender, err := pushauth.NewSignedSender(pushauth.WithJWT())
	require.NoError(t, err)
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()),
		WithPushNotificationAuthenticator(sender.Authenticator()),
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

// TestFinalizePushCapability_RespectsExplicitAndSigned: an explicit capability
// value is never overridden, and a signed card is immutable.
func TestFinalizePushCapability_RespectsExplicitAndSigned(t *testing.T) {
	sender, err := pushauth.NewSignedSender(pushauth.WithJWT())
	require.NoError(t, err)
	tm := newDiscoveryTM(t, memory.WithPushNotifications(sender))

	t.Run("explicit false wins", func(t *testing.T) {
		card := discoveryCard()
		f := false
		card.Capabilities.PushNotifications = &f
		srv, err := NewA2AServer(tm, WithAgentCard(card))
		require.NoError(t, err)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		got := fetchDefaultCard(t, ts)
		require.NotNil(t, got.Capabilities.PushNotifications)
		assert.False(t, *got.Capabilities.PushNotifications, "explicit false must not be overridden")
	})

	t.Run("signed card untouched", func(t *testing.T) {
		card := discoveryCard()
		card.Signatures = []protocol.AgentCardSignature{{Protected: "hdr", Signature: "sig"}}
		srv, err := NewA2AServer(tm, WithAgentCard(card))
		require.NoError(t, err)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		got := fetchDefaultCard(t, ts)
		assert.Nil(t, got.Capabilities.PushNotifications,
			"a signed card is immutable: filling a field would invalidate the JWS")
	})
}

// TestNewA2AServer_IdentityWithoutSenderNoCapability: a signing identity does
// not by itself advertise the push capability — that requires the TaskManager's
// Sender. Without one, the card must not claim pushNotifications, and no JWKS is
// published (which would otherwise contradict config RPCs returning -32003).
func TestNewA2AServer_IdentityWithoutSenderNoCapability(t *testing.T) {
	sender, err := pushauth.NewSignedSender(pushauth.WithJWT())
	require.NoError(t, err)
	tm := newDiscoveryTM(t) // NO push sender wired.

	srv, err := NewA2AServer(tm, WithAgentCard(discoveryCard()),
		WithPushNotificationAuthenticator(sender.Authenticator()))
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
