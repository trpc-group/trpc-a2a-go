// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

// AgentCard is the metadata structure describing an A2A agent (v1.0).
type AgentCard struct {
	Name                             string                  `json:"name"`
	Description                      string                  `json:"description"`
	URL                              string                  `json:"url"`
	Provider                         *AgentProvider          `json:"provider,omitempty"`
	IconURL                          *string                 `json:"iconUrl,omitempty"`
	Version                          string                  `json:"version"`
	DocumentationURL                 *string                 `json:"documentationUrl,omitempty"`
	Capabilities                     AgentCapabilities       `json:"capabilities"`
	SecuritySchemes                  map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Security                         []map[string][]string   `json:"security,omitempty"`
	DefaultInputModes                []string                `json:"defaultInputModes"`
	DefaultOutputModes               []string                `json:"defaultOutputModes"`
	Skills                           []AgentSkill            `json:"skills"`
	SupportsAuthenticatedExtendedCard *bool                  `json:"supportsAuthenticatedExtendedCard,omitempty"`
	PreferredTransport               *string                 `json:"preferredTransport,omitempty"`
	ProtocolVersion                  *string                 `json:"protocolVersion,omitempty"`
	AdditionalInterfaces             []AgentInterface        `json:"additionalInterfaces,omitempty"`
	Signatures                       []AgentCardSignature    `json:"signatures,omitempty"`
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
	Extensions             []AgentExtension `json:"extensions,omitempty"`
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

// AgentInterface provides a declaration of a transport binding.
type AgentInterface struct {
	URL       string `json:"url"`
	Transport string `json:"transport"`
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
