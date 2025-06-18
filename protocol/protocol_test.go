// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package protocol_test provides blackbox tests for the protocol package.
package protocol_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// TestMethodConstants ensures that the RPC method constants are correctly defined
// and maintain their expected values.
func TestMethodConstants(t *testing.T) {
	// Test RPC method constants
	assert.Equal(t, "tasks/send", protocol.MethodTasksSend,
		"MethodTasksSend should be 'tasks/send'")
	assert.Equal(t, "tasks/sendSubscribe", protocol.MethodTasksSendSubscribe,
		"MethodTasksSendSubscribe should be 'tasks/sendSubscribe'")
	assert.Equal(t, "tasks/get", protocol.MethodTasksGet,
		"MethodTasksGet should be 'tasks/get'")
	assert.Equal(t, "tasks/cancel", protocol.MethodTasksCancel,
		"MethodTasksCancel should be 'tasks/cancel'")
	assert.Equal(t, "tasks/pushNotification/set", protocol.MethodTasksPushNotificationSet,
		"MethodTasksPushNotificationSet should be 'tasks/pushNotification/set'")
	assert.Equal(t, "tasks/pushNotification/get", protocol.MethodTasksPushNotificationGet,
		"MethodTasksPushNotificationGet should be 'tasks/pushNotification/get'")
	assert.Equal(t, "tasks/resubscribe", protocol.MethodTasksResubscribe,
		"MethodTasksResubscribe should be 'tasks/resubscribe'")
}

// TestEventTypeConstants ensures that the SSE event type constants are correctly defined
// and maintain their expected values.
func TestEventTypeConstants(t *testing.T) {
	// Test SSE event type constants
	assert.Equal(t, "task_status_update", protocol.EventStatusUpdate,
		"EventTaskStatusUpdate should be 'task_status_update'")
	assert.Equal(t, "task_artifact_update", protocol.EventArtifactUpdate,
		"EventTaskArtifactUpdate should be 'task_artifact_update'")
	assert.Equal(t, "close", protocol.EventClose, "EventClose should be 'close'")
}

// TestEndpointPathConstants ensures that the HTTP endpoint path constants are correctly defined
// and maintain their expected values.
func TestEndpointPathConstants(t *testing.T) {
	// Test HTTP endpoint path constants
	assert.Equal(t, "/.well-known/agent.json", protocol.AgentCardPath,
		"AgentCardPath should be '/.well-known/agent.json'")
	assert.Equal(t, "/.well-known/jwks.json", protocol.JWKSPath, "JWKSPath should be '/.well-known/jwks.json'")
	assert.Equal(t, "/", protocol.DefaultJSONRPCPath, "DefaultJSONRPCPath should be '/'")
}

// TestConstantRelationships checks relationships between related constants
// to ensure protocol coherence.
func TestConstantRelationships(t *testing.T) {
	// Test that push notification methods are properly paired
	assert.True(t, protocol.MethodTasksPushNotificationSet != protocol.MethodTasksPushNotificationGet,
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
	// These tests ensure that key constants follow the patterns defined in the specification

	// All method constants should start with "tasks/"
	methodConstants := []string{
		protocol.MethodTasksSend,
		protocol.MethodTasksSendSubscribe,
		protocol.MethodTasksGet,
		protocol.MethodTasksCancel,
		protocol.MethodTasksPushNotificationSet,
		protocol.MethodTasksPushNotificationGet,
		protocol.MethodTasksResubscribe,
	}

	for _, method := range methodConstants {
		assert.True(t, len(method) >= 6 && method[0:6] == "tasks/",
			"Method %s should start with 'tasks/'", method)
	}

	// Push notification methods should include 'pushNotification' in the path
	pushNotificationMethods := []string{
		protocol.MethodTasksPushNotificationSet,
		protocol.MethodTasksPushNotificationGet,
	}

	for _, method := range pushNotificationMethods {
		assert.Contains(t, method, "pushNotification",
			"Push notification method %s should contain 'pushNotification'", method)
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
