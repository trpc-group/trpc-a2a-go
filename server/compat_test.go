// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestCompatHandler_FillsPublicAgentCard(t *testing.T) {
	card := AgentCard{
		Name:        "compat agent",
		Description: "compat agent",
		Version:     "1.0.0",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             "https://agent.example.com/",
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
	}
	tm := newMockTaskManager()
	srv, err := NewA2AServer(tm,
		WithAgentCard(card),
		WithCompatHandler(v0.NewJSONRPCHandler(tm)),
	)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{protocol.AgentCardPath, protocol.OldAgentCardPath} {
		resp, err := ts.Client().Get(ts.URL + path)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var legacy struct {
			URL             string  `json:"url"`
			ProtocolVersion *string `json:"protocolVersion"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&legacy))
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, "https://agent.example.com/", legacy.URL)
		require.NotNil(t, legacy.ProtocolVersion)
		assert.Equal(t, v0.Version, *legacy.ProtocolVersion)
	}
}

func TestCompatHandler_FillsDynamicTenantAgentCard(t *testing.T) {
	card := AgentCard{
		Name:        "tenant agent",
		Description: "tenant agent",
		Version:     "1.0.0",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             "https://tenant.example.com/",
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
	}
	tm := newMockTaskManager()
	srv, err := NewA2AServer(tm,
		WithTenantCardProvider(func(_ context.Context, tenant string) (AgentCard, error) {
			assert.Equal(t, "tenant-a", tenant)
			return card, nil
		}),
		WithCompatHandler(v0.NewJSONRPCHandler(tm)),
	)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + protocol.AgentCardPath + "?tenant=tenant-a")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()

	var legacy struct {
		URL             string  `json:"url"`
		ProtocolVersion *string `json:"protocolVersion"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&legacy))
	assert.Equal(t, "https://tenant.example.com/", legacy.URL)
	require.NotNil(t, legacy.ProtocolVersion)
	assert.Equal(t, v0.Version, *legacy.ProtocolVersion)
}

func TestCompatHandler_DoesNotMutateSignedAgentCard(t *testing.T) {
	card := AgentCard{
		Name:        "signed agent",
		Description: "signed agent",
		Version:     "1.0.0",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             "https://agent.example.com/a2a",
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Signatures: []protocol.AgentCardSignature{{
			Protected: "protected",
			Signature: "signature",
		}},
	}
	tm := newMockTaskManager()
	srv, err := NewA2AServer(tm,
		WithAgentCard(card),
		WithCompatHandler(v0.NewJSONRPCHandler(tm)),
	)
	require.NoError(t, err)

	resolved, ok := srv.resolveAgentCard(context.Background(), "")
	require.True(t, ok)
	assert.Empty(t, resolved.URL)
	assert.Nil(t, resolved.ProtocolVersion)
	assert.Equal(t, card.Signatures, resolved.Signatures)
}

func TestCompatHandler_DoesNotMutateSignedTenantAgentCard(t *testing.T) {
	card := AgentCard{
		Name:        "signed tenant agent",
		Description: "signed tenant agent",
		Version:     "1.0.0",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             "https://tenant.example.com/",
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Signatures: []protocol.AgentCardSignature{{
			Protected: "protected",
			Signature: "signature",
		}},
	}
	tm := newMockTaskManager()
	srv, err := NewA2AServer(tm,
		WithTenantCard("tenant-a", card),
		WithCompatHandler(v0.NewJSONRPCHandler(tm)),
	)
	require.NoError(t, err)

	resolved, ok := srv.resolveAgentCard(context.Background(), "tenant-a")
	require.True(t, ok)
	assert.Empty(t, resolved.SupportedInterfaces[0].Tenant)
	assert.Empty(t, card.SupportedInterfaces[0].Tenant)
	assert.Equal(t, card.Signatures, resolved.Signatures)
}

func TestCompatHandler_CORS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		want    string
	}{
		{name: "enabled", enabled: true, want: "*"},
		{name: "disabled", enabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compat := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			srv, err := NewA2AServer(newMockTaskManager(),
				WithAgentCard(defaultAgentCard()),
				WithCORSEnabled(tc.enabled),
				WithCompatHandler(compat),
			)
			require.NoError(t, err)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			resp, err := ts.Client().Post(ts.URL, "application/json", bytes.NewBufferString(
				`{"jsonrpc":"2.0","id":"1","method":"message/send","params":{}}`,
			))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tc.want, resp.Header.Get("Access-Control-Allow-Origin"))
		})
	}
}
