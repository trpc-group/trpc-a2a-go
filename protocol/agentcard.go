// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

// AgentCard is the metadata structure describing an A2A agent (v1.0).
//
// The v1.0 wire model describes transport bindings via SupportedInterfaces. The
// deprecated top-level URL/ProtocolVersion/PreferredTransport/AdditionalInterfaces
// and SupportsAuthenticatedExtendedCard fields are retained so a card can be both
// produced and consumed by v0.x clients (dual-format). New code should use
// SupportedInterfaces and Capabilities.ExtendedAgentCard.
type AgentCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`

	// SupportedInterfaces is the v1.0 ordered list of transport bindings; the
	// first entry is preferred. Required by v1.0. When left empty it is derived
	// from the deprecated URL/PreferredTransport/AdditionalInterfaces fields via
	// NormalizeInterfaces.
	SupportedInterfaces []AgentInterface `json:"supportedInterfaces,omitempty"`

	Provider             *AgentProvider            `json:"provider,omitempty"`
	IconURL              *string                   `json:"iconUrl,omitempty"`
	Version              string                    `json:"version"`
	DocumentationURL     *string                   `json:"documentationUrl,omitempty"`
	Capabilities         AgentCapabilities         `json:"capabilities"`
	SecuritySchemes      map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	SecurityRequirements []map[string][]string     `json:"securityRequirements,omitempty"`
	DefaultInputModes    []string                  `json:"defaultInputModes"`
	DefaultOutputModes   []string                  `json:"defaultOutputModes"`
	Skills               []AgentSkill              `json:"skills"`
	Signatures           []AgentCardSignature      `json:"signatures,omitempty"`

	// --- Deprecated v0.x fields (kept for backward compatibility) ---

	// URL is the deprecated single-endpoint URL. Use SupportedInterfaces.
	URL string `json:"url,omitempty"`
	// ProtocolVersion is the deprecated top-level version. Use AgentInterface.ProtocolVersion.
	ProtocolVersion *string `json:"protocolVersion,omitempty"`
	// PreferredTransport is the deprecated preferred binding. Use SupportedInterfaces[0].ProtocolBinding.
	PreferredTransport *string `json:"preferredTransport,omitempty"`
	// AdditionalInterfaces is the deprecated extra-interface list. Use SupportedInterfaces.
	AdditionalInterfaces []AgentInterface `json:"additionalInterfaces,omitempty"`
	// SupportsAuthenticatedExtendedCard is deprecated. Use Capabilities.ExtendedAgentCard.
	SupportsAuthenticatedExtendedCard *bool `json:"supportsAuthenticatedExtendedCard,omitempty"`
}

// ExtendedAgentCardEnabled reports whether the authenticated extended card is
// supported, honoring both the v1.0 location (Capabilities.ExtendedAgentCard)
// and the deprecated top-level flag.
func (c *AgentCard) ExtendedAgentCardEnabled() bool {
	if c.Capabilities.ExtendedAgentCard != nil {
		return *c.Capabilities.ExtendedAgentCard
	}
	return c.SupportsAuthenticatedExtendedCard != nil && *c.SupportsAuthenticatedExtendedCard
}

// PrimaryURL returns the preferred interface URL, falling back to the deprecated
// top-level URL field.
func (c *AgentCard) PrimaryURL() string {
	if len(c.SupportedInterfaces) > 0 {
		return c.SupportedInterfaces[0].URL
	}
	return c.URL
}

// NormalizeInterfaces populates SupportedInterfaces (v1.0) from the deprecated
// URL/PreferredTransport/AdditionalInterfaces fields when it is empty, so a card
// configured the v0 way still serializes a conformant supportedInterfaces list.
func (c *AgentCard) NormalizeInterfaces() {
	if len(c.SupportedInterfaces) > 0 || c.URL == "" {
		return
	}
	binding := "JSONRPC"
	if c.PreferredTransport != nil && *c.PreferredTransport != "" {
		binding = *c.PreferredTransport
	}
	version := "1.0"
	if c.ProtocolVersion != nil && *c.ProtocolVersion != "" {
		version = *c.ProtocolVersion
	}
	c.SupportedInterfaces = append(c.SupportedInterfaces, AgentInterface{
		URL:             c.URL,
		ProtocolBinding: binding,
		ProtocolVersion: version,
	})
	c.SupportedInterfaces = append(c.SupportedInterfaces, c.AdditionalInterfaces...)
}

// AgentProvider contains information about the agent's provider.
type AgentProvider struct {
	Organization string  `json:"organization"`
	URL          *string `json:"url,omitempty"`
}

// AgentCapabilities defines the capabilities supported by an agent.
type AgentCapabilities struct {
	Streaming              *bool            `json:"streaming,omitempty"`
	PushNotifications      *bool            `json:"pushNotifications,omitempty"`
	StateTransitionHistory *bool            `json:"stateTransitionHistory,omitempty"`
	// ExtendedAgentCard reports whether the agent serves an authenticated
	// extended card (v1.0 location for the deprecated
	// AgentCard.SupportsAuthenticatedExtendedCard flag).
	ExtendedAgentCard *bool            `json:"extendedAgentCard,omitempty"`
	Extensions        []AgentExtension `json:"extensions,omitempty"`
}

// AgentSkill describes a specific capability of the agent.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// AgentExtension represents an agent extension.
type AgentExtension struct {
	URI         string         `json:"uri"`
	Required    *bool          `json:"required,omitempty"`
	Description *string        `json:"description,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

// AgentInterface provides a declaration of a transport binding (v1.0).
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	Tenant          string `json:"tenant,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// AgentCardSignature represents a JWS signature of an AgentCard (RFC 7515).
type AgentCardSignature struct {
	Header    map[string]any `json:"header,omitempty"`
	Protected string         `json:"protected"`
	Signature string         `json:"signature"`
}

// AgentAuthentication defines the authentication mechanism required by the agent.
type AgentAuthentication struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Config   any    `json:"config,omitempty"`
}
