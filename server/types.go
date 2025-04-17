// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package server contains the A2A server implementation and related types.
package server

// AgentCapabilities defines the capabilities supported by an agent.
type AgentCapabilities struct {
	// Streaming is a flag indicating if the agent supports streaming responses.
	Streaming bool `json:"streaming"`
	// PushNotifications is a flag indicating if the agent can push notifications.
	PushNotifications bool `json:"pushNotifications"`
	// StateTransitionHistory is a flag indicating if the agent can provide task history.
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

// AgentSkill describes a specific capability or function of the agent.
type AgentSkill struct {
	// ID is the unique identifier for the skill.
	ID string `json:"id"`
	// Name is the human-readable name of the skill.
	Name string `json:"name"`
	// Description is an optional detailed description of the skill.
	Description *string `json:"description,omitempty"`
	// Tags are optional tags for categorization.
	Tags []string `json:"tags,omitempty"`
	// Examples are optional usage examples.
	Examples []string `json:"examples,omitempty"`
	// InputModes are the supported input data modes/types.
	InputModes []string `json:"inputModes,omitempty"`
	// OutputModes are the supported output data modes/types.
	OutputModes []string `json:"outputModes,omitempty"`
}

// AgentProvider contains information about the agent's provider or developer.
type AgentProvider struct {
	// Name is the name of the provider.
	Name string `json:"name"`
	// URL is an optional URL for the provider.
	URL *string `json:"url,omitempty"`
}

// AgentAuthentication defines the authentication mechanism required by the agent.
type AgentAuthentication struct {
	// Type is the type of authentication (e.g., "none", "apiKey", "oauth").
	Type string `json:"type"`
	// Required is a flag indicating if authentication is mandatory.
	Required bool `json:"required"`
	// Config is an optional configuration details for the auth type.
	Config interface{} `json:"config,omitempty"`
}

// AgentCard is the metadata structure describing an A2A agent.
// This is typically returned by the agent_get_card method.
type AgentCard struct {
	// Name is the name of the agent.
	Name string `json:"name"`
	// Description is an optional description of the agent.
	Description *string `json:"description,omitempty"`
	// URL is the endpoint URL where the agent is hosted.
	URL string `json:"url"`
	// Provider is an optional provider information.
	Provider *AgentProvider `json:"provider,omitempty"`
	// Version is the agent version string.
	Version string `json:"version"`
	// DocumentationURL is an optional link to documentation.
	DocumentationURL *string `json:"documentationUrl,omitempty"`
	// Capabilities are the declared capabilities of the agent.
	Capabilities AgentCapabilities `json:"capabilities"`
	// Authentication is an optional authentication details.
	Authentication *AgentAuthentication `json:"authentication,omitempty"`
	// DefaultInputModes are the default input modes if not specified per skill.
	DefaultInputModes []string `json:"defaultInputModes"`
	// DefaultOutputModes are the default output modes if not specified per skill.
	DefaultOutputModes []string `json:"defaultOutputModes"`
	// Skills are optional list of specific skills.
	Skills []AgentSkill `json:"skills,omitempty"`
}
