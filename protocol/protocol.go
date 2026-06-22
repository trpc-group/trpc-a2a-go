// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package protocol defines constants and potentially shared types for the A2A protocol itself.
package protocol

// Method names for the A2A v1.0 specification (PascalCase, per the JSON-RPC binding).
//
// NOTE: the Go constant identifiers are kept stable (MethodMessageSend, ...) to
// minimize churn in server/client code; only the wire values changed to v1.0
// PascalCase. The legacy slash-delimited names (message/send, ...) live in the
// compat/v0 package, where they are disjoint from these and can be routed
// independently on the same endpoint.
const (
	MethodMessageSend                    = "SendMessage"
	MethodMessageStream                  = "SendStreamingMessage"
	MethodTasksGet                       = "GetTask"
	MethodTasksCancel                    = "CancelTask"
	MethodTasksPushNotificationConfigSet = "CreateTaskPushNotificationConfig"
	MethodTasksPushNotificationConfigGet = "GetTaskPushNotificationConfig"
	MethodTasksResubscribe               = "SubscribeToTask"
	MethodAgentAuthenticatedExtendedCard = "GetExtendedAgentCard"

	// New operations introduced in v1.0 (additive; old clients never call these).
	MethodTasksList                         = "ListTasks"
	MethodTasksPushNotificationConfigList   = "ListTaskPushNotificationConfigs"
	MethodTasksPushNotificationConfigDelete = "DeleteTaskPushNotificationConfig"
)

// SSE event type strings used in A2A SSE streams.
const (
	EventStatusUpdate   = "statusUpdate"
	EventArtifactUpdate = "artifactUpdate"
	EventTask           = "task"
	EventMessage        = "message"

	// EventClose is used internally to signal stream closure (not part of the A2A spec).
	EventClose = "close"
)

// HTTP Endpoint Paths.
const (
	AgentCardPath      = "/.well-known/agent-card.json"
	OldAgentCardPath   = "/.well-known/agent.json"
	JWKSPath           = "/.well-known/jwks.json"
	DefaultJSONRPCPath = "/"
)
