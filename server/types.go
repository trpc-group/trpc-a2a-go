// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package server contains the A2A server implementation and related types.
package server

import "trpc.group/trpc-go/trpc-a2a-go/protocol"

const (
	ProtocolVersion = "1.0.1"
)

// Type aliases re-exported from protocol/ for backward compatibility.
// New code should import protocol/ directly.
type (
	AgentCard          = protocol.AgentCard
	AgentProvider      = protocol.AgentProvider
	AgentCapabilities  = protocol.AgentCapabilities
	AgentSkill         = protocol.AgentSkill
	AgentExtension     = protocol.AgentExtension
	AgentInterface     = protocol.AgentInterface
	AgentCardSignature = protocol.AgentCardSignature
	SecurityScheme     = protocol.SecurityScheme
	SecuritySchemeType = protocol.SecuritySchemeType
	SecuritySchemeIn   = protocol.SecuritySchemeIn
	OAuthFlows         = protocol.OAuthFlows
	OAuthFlow          = protocol.OAuthFlow
	AgentAuthentication = protocol.AgentAuthentication
)

// Re-export security scheme constants for backward compatibility.
const (
	SecuritySchemeTypeAPIKey        = protocol.SecuritySchemeTypeAPIKey
	SecuritySchemeTypeHTTP          = protocol.SecuritySchemeTypeHTTP
	SecuritySchemeTypeOAuth2        = protocol.SecuritySchemeTypeOAuth2
	SecuritySchemeTypeOpenIDConnect = protocol.SecuritySchemeTypeOpenIDConnect
	SecuritySchemeInQuery           = protocol.SecuritySchemeInQuery
	SecuritySchemeInHeader          = protocol.SecuritySchemeInHeader
	SecuritySchemeInCookie          = protocol.SecuritySchemeInCookie
)
