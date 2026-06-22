// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package protocol_test provides blackbox tests for the protocol package.
package protocol_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// TestMethodConstants ensures that the RPC method constants are correctly defined
// and maintain their expected values.
func TestMethodConstants(t *testing.T) {
	// Test RPC method constants
	assert.Equal(t, "SendMessage", protocol.MethodMessageSend,
		"MethodMessageSend should be 'SendMessage'")
	assert.Equal(t, "SendStreamingMessage", protocol.MethodMessageStream,
		"MethodMessageStream should be 'SendStreamingMessage'")
	assert.Equal(t, "GetTask", protocol.MethodTasksGet,
		"MethodTasksGet should be 'GetTask'")
	assert.Equal(t, "CancelTask", protocol.MethodTasksCancel,
		"MethodTasksCancel should be 'CancelTask'")
	assert.Equal(t, "CreateTaskPushNotificationConfig", protocol.MethodTasksPushNotificationConfigSet,
		"MethodTasksPushNotificationConfigSet should be 'CreateTaskPushNotificationConfig'")
	assert.Equal(t, "GetTaskPushNotificationConfig", protocol.MethodTasksPushNotificationConfigGet,
		"MethodTasksPushNotificationConfigGet should be 'GetTaskPushNotificationConfig'")
	assert.Equal(t, "SubscribeToTask", protocol.MethodTasksResubscribe,
		"MethodTasksResubscribe should be 'SubscribeToTask'")
}

// TestEventTypeConstants ensures that the SSE event type constants are correctly defined
// and maintain their expected values.
func TestEventTypeConstants(t *testing.T) {
	// Test SSE event type constants (v1.0 uses camelCase)
	assert.Equal(t, "statusUpdate", protocol.EventStatusUpdate)
	assert.Equal(t, "artifactUpdate", protocol.EventArtifactUpdate)
	assert.Equal(t, "close", protocol.EventClose)
}

// TestEndpointPathConstants ensures that the HTTP endpoint path constants are correctly defined
// and maintain their expected values.
func TestEndpointPathConstants(t *testing.T) {
	// Test HTTP endpoint path constants
	assert.Equal(t, "/.well-known/agent-card.json", protocol.AgentCardPath,
		"AgentCardPath should be '/.well-known/agent-card.json'")
	assert.Equal(t, "/.well-known/jwks.json", protocol.JWKSPath, "JWKSPath should be '/.well-known/jwks.json'")
	assert.Equal(t, "/", protocol.DefaultJSONRPCPath, "DefaultJSONRPCPath should be '/'")
}

// TestConstantRelationships checks relationships between related constants
// to ensure protocol coherence.
func TestConstantRelationships(t *testing.T) {
	// Test that push notification methods are properly paired
	assert.True(t, protocol.MethodTasksPushNotificationConfigSet != protocol.MethodTasksPushNotificationConfigGet,
		"Push notification set and get methods should be distinct")

	// Test that event types are distinct
	assert.True(t, protocol.EventStatusUpdate != protocol.EventArtifactUpdate,
		"Status and artifact event types should be distinct")
	assert.True(t, protocol.EventStatusUpdate != protocol.EventClose,
		"Status update and close event types should be distinct")
	assert.True(t, protocol.EventArtifactUpdate != protocol.EventClose,
		"Artifact update and close event types should be distinct")

	// Test that HTTP endpoint paths are distinct
	assert.True(t, protocol.AgentCardPath != protocol.JWKSPath,
		"Agent card and JWKS paths should be distinct")
	assert.True(t, protocol.AgentCardPath != protocol.DefaultJSONRPCPath,
		"Agent card and JSON-RPC paths should be distinct")
	assert.True(t, protocol.JWKSPath != protocol.DefaultJSONRPCPath,
		"JWKS and JSON-RPC paths should be distinct")
}

// TestConsistencyWithSpecification tests that our implementation's constants
// align with the A2A protocol specification.
func TestConsistencyWithSpecification(t *testing.T) {
	// These tests ensure that key constants follow the v1.0 specification patterns.

	// v1.0 JSON-RPC method names are PascalCase (no slash delimiters).
	v1Methods := []string{
		protocol.MethodMessageSend,
		protocol.MethodMessageStream,
		protocol.MethodTasksGet,
		protocol.MethodTasksCancel,
		protocol.MethodTasksResubscribe,
		protocol.MethodTasksList,
		protocol.MethodTasksPushNotificationConfigSet,
		protocol.MethodTasksPushNotificationConfigGet,
		protocol.MethodTasksPushNotificationConfigList,
		protocol.MethodTasksPushNotificationConfigDelete,
	}

	for _, method := range v1Methods {
		assert.NotContains(t, method, "/",
			"v1.0 method %s must not contain '/' (PascalCase per spec)", method)
		assert.NotEmpty(t, method)
		// First character should be an uppercase ASCII letter.
		assert.True(t, method[0] >= 'A' && method[0] <= 'Z',
			"v1.0 method %s should start with an uppercase letter", method)
	}

	// Message-related methods should be exactly the v1.0 names.
	messageMethods := map[string]bool{
		protocol.MethodMessageSend:   true,
		protocol.MethodMessageStream: true,
	}
	expectedMessageMethods := map[string]bool{
		"SendMessage":          true,
		"SendStreamingMessage": true,
	}
	assert.Equal(t, expectedMessageMethods, messageMethods, "Unexpected message methods")

	// Push notification config methods should include 'PushNotificationConfig'.
	pushNotificationMethods := []string{
		protocol.MethodTasksPushNotificationConfigSet,
		protocol.MethodTasksPushNotificationConfigGet,
		protocol.MethodTasksPushNotificationConfigList,
		protocol.MethodTasksPushNotificationConfigDelete,
	}
	for _, method := range pushNotificationMethods {
		assert.Contains(t, method, "PushNotificationConfig",
			"Push notification method %s should contain 'PushNotificationConfig'", method)
	}

	// Well-known paths should start with '/.well-known/'
	wellKnownPaths := []string{
		protocol.AgentCardPath,
		protocol.JWKSPath,
	}

	for _, path := range wellKnownPaths {
		assert.True(t, len(path) >= 13 && path[0:13] == "/.well-known/",
			"Well-known path %s should start with '/.well-known/'", path)
	}
}
