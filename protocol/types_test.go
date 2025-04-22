// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"testing"
	"time"

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

// TestTaskState tests the task state constants
func TestTaskState(t *testing.T) {
	tests := []struct {
		state    TaskState
		expected string
	}{
		{TaskStateSubmitted, "submitted"},
		{TaskStateWorking, "working"},
		{TaskStateCompleted, "completed"},
		{TaskStateCanceled, "canceled"},
		{TaskStateFailed, "failed"},
		{TaskStateUnknown, "unknown"},
		{TaskStateInputRequired, "input-required"},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			assert.Equal(t, test.expected, string(test.state))
		})
	}
}

// TestMessageJSON tests the JSON marshaling and unmarshaling of Message
func TestMessageJSON(t *testing.T) {
	// Create text part
	textPart := TextPart{
		Type: PartTypeText,
		Text: "Hello, world!",
	}

	// Create file part
	filePart := FilePart{
		Type: PartTypeFile,
		File: FileContent{
			Name:     stringPtr("example.txt"),
			MimeType: stringPtr("text/plain"),
			Bytes:    stringPtr("RmlsZSBjb250ZW50"), // Base64 encoded "File content"
		},
	}

	// Create a message with these parts
	original := NewMessage(MessageRoleUser, []Part{textPart, filePart})

	// Marshal the message
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal back to a message
	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify the role
	assert.Equal(t, MessageRoleUser, decoded.Role)

	// Verify we have two parts
	require.Len(t, decoded.Parts, 2)

	// Check the text part
	textPartDecoded, ok := decoded.Parts[0].(TextPart)
	require.True(t, ok, "First part should be a TextPart")
	assert.Equal(t, PartTypeText, textPartDecoded.Type)
	assert.Equal(t, "Hello, world!", textPartDecoded.Text)

	// Check the file part
	filePartDecoded, ok := decoded.Parts[1].(FilePart)
	require.True(t, ok, "Second part should be a FilePart")
	assert.Equal(t, PartTypeFile, filePartDecoded.Type)
	assert.Equal(t, "example.txt", *filePartDecoded.File.Name)
	assert.Equal(t, "text/plain", *filePartDecoded.File.MimeType)
	assert.Equal(t, "RmlsZSBjb250ZW50", *filePartDecoded.File.Bytes)
}

// TestPartValidation tests validation of different part types
func TestPartValidation(t *testing.T) {
	// Test TextPart
	t.Run("TextPart", func(t *testing.T) {
		textPart := TextPart{
			Type: PartTypeText,
			Text: "Valid text content",
		}
		assert.True(t, isValidPart(textPart))

		// Invalid part type
		invalidPart := TextPart{
			Type: "invalid",
			Text: "Text with invalid part type",
		}
		assert.False(t, isValidPart(invalidPart))
	})

	// Test FilePart
	t.Run("FilePart", func(t *testing.T) {
		validFilePart := FilePart{
			Type: PartTypeFile,
			File: FileContent{
				Name:     stringPtr("file.txt"),
				MimeType: stringPtr("text/plain"),
				Bytes:    stringPtr("SGVsbG8="), // Base64 "Hello"
			},
		}
		assert.True(t, isValidPart(validFilePart))

		// Invalid part: missing required file info
		invalidFilePart := FilePart{
			Type: PartTypeFile,
			File: FileContent{}, // Empty file content
		}
		assert.False(t, isValidPart(invalidFilePart))
	})

	// Test DataPart
	t.Run("DataPart", func(t *testing.T) {
		validDataPart := DataPart{
			Type: PartTypeData,
			Data: map[string]interface{}{"key": "value"},
		}
		assert.True(t, isValidPart(validDataPart))

		// Invalid part: nil data
		invalidDataPart := DataPart{
			Type: PartTypeData,
			Data: nil,
		}
		assert.False(t, isValidPart(invalidDataPart))
	})
}

// isValidPart is a helper function to check if a part is valid
// This is a simplified validation just for testing
func isValidPart(part Part) bool {
	switch p := part.(type) {
	case TextPart:
		return p.Type == PartTypeText && p.Text != ""
	case FilePart:
		return p.Type == PartTypeFile && (p.File.Name != nil || p.File.URI != nil || p.File.Bytes != nil)
	case DataPart:
		return p.Type == PartTypeData && p.Data != nil
	default:
		return false
	}
}

// TestArtifact tests the artifact functionality
func TestArtifact(t *testing.T) {
	// Create a simple text part
	textPart := TextPart{
		Type: PartTypeText,
		Text: "Artifact content",
	}

	// Create an artifact
	artifact := Artifact{
		Name:        stringPtr("Test Artifact"),
		Description: stringPtr("This is a test artifact"),
		Parts:       []Part{textPart},
		Index:       1,
		LastChunk:   boolPtr(true),
	}

	// Validate the artifact
	assert.NotNil(t, artifact.Name)
	assert.Equal(t, "Test Artifact", *artifact.Name)
	assert.NotNil(t, artifact.Description)
	assert.Equal(t, "This is a test artifact", *artifact.Description)
	assert.Len(t, artifact.Parts, 1)
	assert.Equal(t, 1, artifact.Index)
	assert.NotNil(t, artifact.LastChunk)
	assert.True(t, *artifact.LastChunk)

	// Test JSON marshaling
	data, err := json.Marshal(artifact)
	require.NoError(t, err)

	// Test JSON unmarshaling
	var decoded Artifact
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify the decoded artifact
	assert.Equal(t, *artifact.Name, *decoded.Name)
	assert.Equal(t, *artifact.Description, *decoded.Description)
	assert.Equal(t, artifact.Index, decoded.Index)
	assert.Equal(t, *artifact.LastChunk, *decoded.LastChunk)

	// Check the part
	require.Len(t, decoded.Parts, 1)
	decodedPart, ok := decoded.Parts[0].(TextPart)
	require.True(t, ok, "Should decode as a TextPart")
	assert.Equal(t, PartTypeText, decodedPart.Type)
	assert.Equal(t, "Artifact content", decodedPart.Text)
}

// TestTaskStatus tests the TaskStatus type
func TestTaskStatus(t *testing.T) {
	now := time.Now().Format(time.RFC3339)

	// Create a task status
	status := TaskStatus{
		State:     TaskStateCompleted,
		Timestamp: now,
	}

	// Validate the fields
	assert.Equal(t, TaskStateCompleted, status.State)
	assert.Equal(t, now, status.Timestamp)

	// Test adding a message to the status
	message := NewMessage(MessageRoleAgent, []Part{
		TextPart{
			Type: PartTypeText,
			Text: "Task completed successfully",
		},
	})

	status.Message = &message
	assert.NotNil(t, status.Message)
	assert.Equal(t, MessageRoleAgent, status.Message.Role)
}

// Helper functions for testing
func stringPtr(s string) *string {
	return &s
}

// TestMarkerFunctions tests the marker functions for parts and events
func TestMarkerFunctions(t *testing.T) {
	// Test part marker functions
	textPart := TextPart{Type: PartTypeText, Text: "Test"}
	filePart := FilePart{Type: PartTypeFile}
	dataPart := DataPart{Type: PartTypeData, Data: map[string]string{"key": "value"}}

	// This test simply ensures the marker functions exist and don't panic
	// They're just marker methods with no behavior
	textPart.partMarker()
	filePart.partMarker()
	dataPart.partMarker()

	// Test event marker functions
	statusEvent := TaskStatusUpdateEvent{ID: "test", Status: TaskStatus{State: TaskStateCompleted}}
	artifactEvent := TaskArtifactUpdateEvent{ID: "test"}

	// This test simply ensures the marker functions exist and don't panic
	statusEvent.eventMarker()
	artifactEvent.eventMarker()

	// Verify these functions don't have any observable behavior
	// This is just to increase test coverage, as these are marker functions
}

// TestNewTask tests the NewTask factory function
func TestNewTask(t *testing.T) {
	// Test with just ID
	taskID := "test-task"
	task := NewTask(taskID, nil)

	assert.Equal(t, taskID, task.ID)
	assert.Nil(t, task.SessionID)
	assert.Equal(t, TaskStateSubmitted, task.Status.State)
	assert.NotEmpty(t, task.Status.Timestamp)
	assert.NotNil(t, task.Metadata)
	assert.Empty(t, task.Metadata)
	assert.Nil(t, task.Artifacts)

	// Test with session ID
	sessionID := "test-session"
	task = NewTask(taskID, &sessionID)

	assert.Equal(t, taskID, task.ID)
	assert.Equal(t, &sessionID, task.SessionID)
	assert.Equal(t, *task.SessionID, sessionID)
}
