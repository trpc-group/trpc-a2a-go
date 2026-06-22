// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package server contains the A2A server implementation and related types.
package server

import "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"

// ProtocolVersion is the A2A protocol version implemented by this server.
const ProtocolVersion = "1.0"

// The following types are aliases re-exported from protocol/ for backward
// compatibility. New code should import protocol/ directly.
type (
	// AgentCard is an alias for protocol.AgentCard.
	AgentCard = protocol.AgentCard
	// AgentProvider is an alias for protocol.AgentProvider.
	AgentProvider = protocol.AgentProvider
	// AgentCapabilities is an alias for protocol.AgentCapabilities.
	AgentCapabilities = protocol.AgentCapabilities
	// AgentSkill is an alias for protocol.AgentSkill.
	AgentSkill = protocol.AgentSkill
	// AgentExtension is an alias for protocol.AgentExtension.
	AgentExtension = protocol.AgentExtension
	// AgentInterface is an alias for protocol.AgentInterface.
	AgentInterface = protocol.AgentInterface
	// AgentCardSignature is an alias for protocol.AgentCardSignature.
	AgentCardSignature = protocol.AgentCardSignature
	// SecurityScheme is an alias for protocol.SecurityScheme.
	SecurityScheme = protocol.SecurityScheme
	// SecuritySchemeType is an alias for protocol.SecuritySchemeType.
	SecuritySchemeType = protocol.SecuritySchemeType
	// SecuritySchemeIn is an alias for protocol.SecuritySchemeIn.
	SecuritySchemeIn = protocol.SecuritySchemeIn
	// OAuthFlows is an alias for protocol.OAuthFlows.
	OAuthFlows = protocol.OAuthFlows
	// OAuthFlow is an alias for protocol.OAuthFlow.
	OAuthFlow = protocol.OAuthFlow
	// AgentAuthentication is an alias for protocol.AgentAuthentication.
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
