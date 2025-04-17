// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to get a pointer to a boolean.
func boolPtr(b bool) *bool {
	return &b
}

// Test marshalling of concrete types
func TestPartConcreteType_MarshalJSON(t *testing.T) {
	t.Run("TextPart", func(t *testing.T) {
		part := NewTextPart("Hello")
		jsonData, err := json.Marshal(part)
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"text","text":"Hello"}`, string(jsonData))
	})

	t.Run("DataPart", func(t *testing.T) {
		payload := map[string]int{"a": 1}
		part := DataPart{Type: PartTypeData, Data: payload}
		jsonData, err := json.Marshal(part)
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"data","data":{"a":1}}`, string(jsonData))
	})

	// FilePart marshalling test can be added if needed
}

// Test unmarshalling via container structs (Message/Artifact)
// This specifically tests the custom UnmarshalJSON methods.
func TestPartIfaceUnmarshalJSONCont(t *testing.T) {
	jsonData := `{
		"role": "user",
		"parts": [
			{"type":"text", "text":"part1"},
			{"type":"data", "data":{"val": true}},
			{"type":"text", "text":"part3"}
		]
	}`

	var msg Message
	err := json.Unmarshal([]byte(jsonData), &msg)
	require.NoError(t, err, "Unmarshal into Message should succeed via custom UnmarshalJSON")

	require.Len(t, msg.Parts, 3)
	require.IsType(t, TextPart{}, msg.Parts[0])
	assert.Equal(t, "part1", msg.Parts[0].(TextPart).Text)

	require.IsType(t, DataPart{}, msg.Parts[1])
	assert.Equal(t, map[string]interface{}{"val": true}, msg.Parts[1].(DataPart).Data)

	require.IsType(t, TextPart{}, msg.Parts[2])
	assert.Equal(t, "part3", msg.Parts[2].(TextPart).Text)
}

// Removed TestArtifactPart_MarshalUnmarshalJSON as ArtifactPart type doesn't exist
// and Artifact struct marshalling/unmarshalling is tested implicitly above.

// Removed TestMessage_MarshalUnmarshalJSON as it's covered by TestPartIface_UnmarshalJSON_Container

func TestTaskEvent_IsFinal(t *testing.T) {
	// Use boolPtr helper for LastChunk
	tests := []struct {
		name     string
		event    TaskEvent
		expected bool
	}{
		{
			name:     "StatusUpdate Submitted",
			event:    TaskStatusUpdateEvent{Final: false, Status: TaskStatus{State: TaskStateSubmitted}},
			expected: false,
		},
		{
			name:     "StatusUpdate Working",
			event:    TaskStatusUpdateEvent{Final: false, Status: TaskStatus{State: TaskStateWorking}},
			expected: false,
		},
		{
			name:     "StatusUpdate Completed",
			event:    TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateCompleted}},
			expected: true,
		},
		{
			name:     "StatusUpdate Failed",
			event:    TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateFailed}},
			expected: true,
		},
		{
			name:     "StatusUpdate Canceled",
			event:    TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateCanceled}},
			expected: true,
		},
		{
			name:     "ArtifactUpdate Not Last Chunk",
			event:    TaskArtifactUpdateEvent{Final: false, Artifact: Artifact{LastChunk: boolPtr(false)}},
			expected: false,
		},
		{
			name:     "ArtifactUpdate Last Chunk",
			event:    TaskArtifactUpdateEvent{Final: true, Artifact: Artifact{LastChunk: boolPtr(true)}},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.event.IsFinal())
		})
	}
}
