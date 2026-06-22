// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecurityScheme_SupersetWire(t *testing.T) {
	in := SecuritySchemeInHeader
	s := SecurityScheme{
		Type: SecuritySchemeTypeAPIKey,
		Name: stringPtr("X-API-Key"),
		In:   &in,
	}
	b, err := json.Marshal(s)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	// v0 flat fields present.
	assert.Equal(t, "apiKey", m["type"])
	assert.Equal(t, "header", m["in"])
	// v1.0 oneof wrapper present.
	wrapper, ok := m["apiKeySecurityScheme"].(map[string]any)
	require.True(t, ok, "expected apiKeySecurityScheme wrapper, got %v", m)
	assert.Equal(t, "header", wrapper["location"])
	assert.Equal(t, "X-API-Key", wrapper["name"])
}

func TestSecurityScheme_UnmarshalBothShapes(t *testing.T) {
	// v1.0 wrapper shape.
	var s1 SecurityScheme
	require.NoError(t, json.Unmarshal([]byte(`{"apiKeySecurityScheme":{"location":"query","name":"k"}}`), &s1))
	assert.Equal(t, SecuritySchemeTypeAPIKey, s1.Type)
	require.NotNil(t, s1.In)
	assert.Equal(t, SecuritySchemeInQuery, *s1.In)
	assert.Equal(t, "k", *s1.Name)

	// v0 flat shape.
	var s2 SecurityScheme
	require.NoError(t, json.Unmarshal([]byte(`{"type":"http","scheme":"bearer","bearerFormat":"JWT"}`), &s2))
	assert.Equal(t, SecuritySchemeTypeHTTP, s2.Type)
	assert.Equal(t, "bearer", *s2.Scheme)
}

func TestSecurityRequirements_WrappedWire(t *testing.T) {
	r := SecurityRequirements{{"apiKey": {"read"}}}
	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"schemes":{"apiKey":["read"]}}]`, string(b))

	// Unmarshal both shapes.
	var fromV1 SecurityRequirements
	require.NoError(t, json.Unmarshal([]byte(`[{"schemes":{"k":["s"]}}]`), &fromV1))
	require.Len(t, fromV1, 1)
	assert.Equal(t, []string{"s"}, fromV1[0]["k"])

	var fromV0 SecurityRequirements
	require.NoError(t, json.Unmarshal([]byte(`[{"k":["s"]}]`), &fromV0))
	require.Len(t, fromV0, 1)
	assert.Equal(t, []string{"s"}, fromV0[0]["k"])
}

func TestAgentInterface_DualKeyWire(t *testing.T) {
	i := AgentInterface{URL: "https://x", ProtocolBinding: "GRPC", ProtocolVersion: "1.0"}
	b, err := json.Marshal(i)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "GRPC", m["protocolBinding"])
	assert.Equal(t, "GRPC", m["transport"], "v0 transport key must mirror protocolBinding")

	// Reading a v0-only card (transport, no protocolBinding) keeps the binding.
	var back AgentInterface
	require.NoError(t, json.Unmarshal([]byte(`{"url":"https://y","transport":"JSONRPC"}`), &back))
	assert.Equal(t, "JSONRPC", back.ProtocolBinding)
}

func TestAgentCard_NormalizeSecurity_Mirrors(t *testing.T) {
	c := AgentCard{SecurityRequirements: SecurityRequirements{{"k": {"s"}}}}
	c.NormalizeSecurity()
	require.Len(t, c.Security, 1)
	assert.Equal(t, []string{"s"}, c.Security[0]["k"])
}

func TestAgentCard_NormalizeInterfaces_DerivedVersionIsV1(t *testing.T) {
	// A dual-format card whose top-level ProtocolVersion is the legacy value
	// must still derive a v1.0 interface version (regression for the
	// FillLegacyCardFields/NormalizeInterfaces ordering bug).
	legacy := "0.2.5"
	c := AgentCard{URL: "https://x", ProtocolVersion: &legacy}
	c.NormalizeInterfaces()
	require.Len(t, c.SupportedInterfaces, 1)
	assert.Equal(t, "1.0", c.SupportedInterfaces[0].ProtocolVersion)
}
