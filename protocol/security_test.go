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
	// SecurityRequirement.schemes is map<string, StringList> in the proto, so
	// the ProtoJSON form wraps the scopes in {"list": [...]}.
	r := SecurityRequirements{{"apiKey": {"read"}}}
	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"schemes":{"apiKey":{"list":["read"]}}}]`, string(b))

	// The spec's own sample Agent Card must round-trip.
	var fromSpec SecurityRequirements
	require.NoError(t, json.Unmarshal(
		[]byte(`[{"schemes":{"google":{"list":["openid","profile","email"]}}}]`), &fromSpec))
	require.Len(t, fromSpec, 1)
	assert.Equal(t, []string{"openid", "profile", "email"}, fromSpec[0]["google"])

	// Bare scopes inside "schemes" — what earlier v2 prereleases emitted.
	var fromPrerelease SecurityRequirements
	require.NoError(t, json.Unmarshal([]byte(`[{"schemes":{"k":["s"]}}]`), &fromPrerelease))
	require.Len(t, fromPrerelease, 1)
	assert.Equal(t, []string{"s"}, fromPrerelease[0]["k"])

	var fromV0 SecurityRequirements
	require.NoError(t, json.Unmarshal([]byte(`[{"k":["s"]}]`), &fromV0))
	require.Len(t, fromV0, 1)
	assert.Equal(t, []string{"s"}, fromV0[0]["k"])

	// "schemes" is also a valid v0 scheme name. An array value distinguishes
	// it from the v1.0 wrapper object.
	var namedSchemes SecurityRequirements
	require.NoError(t, json.Unmarshal(
		[]byte(`[{"schemes":["read"]},{"schemes":[],"oauth":["openid"]}]`), &namedSchemes))
	require.Len(t, namedSchemes, 2)
	assert.Equal(t, []string{"read"}, namedSchemes[0]["schemes"])
	assert.Empty(t, namedSchemes[1]["schemes"])
	assert.Equal(t, []string{"openid"}, namedSchemes[1]["oauth"])
}

func TestSecurityRequirements_MixedCompatibilityWire(t *testing.T) {
	var got SecurityRequirements
	require.NoError(t, json.Unmarshal([]byte(`[
		{"schemes":{"oauth":{"list":["openid"]}}},
		{"schemes":{"apiKey":["read"]}},
		{"mtls":[]}
	]`), &got))
	require.Len(t, got, 3)
	assert.Equal(t, []string{"openid"}, got[0]["oauth"])
	assert.Equal(t, []string{"read"}, got[1]["apiKey"])
	assert.Empty(t, got[2]["mtls"])
}

func TestSecurityRequirements_IgnoresUnknownFields(t *testing.T) {
	var got SecurityRequirements
	require.NoError(t, json.Unmarshal([]byte(`[
		{"schemes":{"oauth":{"list":["openid"],"future":true}},"future":"ignored"},
		{"schemes":{"apiKey":["read"]},"future":"ignored"},
		{"mtls":[]},
		{"schemes":{"futureOnly":{"future":true}}},
		{"future":true},
		{"schemes":null,"future":true}
	]`), &got))
	require.Len(t, got, 6)
	assert.Equal(t, []string{"openid"}, got[0]["oauth"])
	assert.Equal(t, []string{"read"}, got[1]["apiKey"])
	assert.Empty(t, got[2]["mtls"])
	assert.Empty(t, got[3]["futureOnly"])
	assert.Empty(t, got[4])
	assert.Empty(t, got[5])
}

func TestSecurityRequirements_RejectsMalformedScopes(t *testing.T) {
	for _, input := range []string{
		`[{"schemes":{"oauth":{"list":"openid"}}}]`,
		`[{"schemes":"oauth"}]`,
	} {
		var got SecurityRequirements
		assert.Error(t, json.Unmarshal([]byte(input), &got), input)
	}
}

func TestAgentSkill_SecurityRequirementsRoundTrip(t *testing.T) {
	want := AgentSkill{
		ID: "skill", Name: "Skill", Tags: []string{"test"},
		SecurityRequirements: SecurityRequirements{{"oauth": {"openid"}}},
	}
	payload, err := json.Marshal(want)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"securityRequirements":[{"schemes":{"oauth":{"list":["openid"]}}}]`)
	var got AgentSkill
	require.NoError(t, json.Unmarshal(payload, &got))
	assert.Equal(t, want.SecurityRequirements, got.SecurityRequirements)
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
