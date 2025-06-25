// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to get a pointer to a boolean.
func boolPtr(b bool) *bool {
	return &b
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}

// Test marshalling of concrete types
func TestPartConcreteType_MarshalJSON(t *testing.T) {
	t.Run("TextPart", func(t *testing.T) {
		part := NewTextPart("Hello")
		jsonData, err := json.Marshal(part)
		require.NoError(t, err)
		assert.JSONEq(t, `{"kind":"text","text":"Hello"}`, string(jsonData))
	})

	t.Run("DataPart", func(t *testing.T) {
		payload := map[string]int{"a": 1}
		part := DataPart{Kind: KindData, Data: payload}
		jsonData, err := json.Marshal(part)
		require.NoError(t, err)
		assert.JSONEq(t, `{"kind":"data","data":{"a":1}}`, string(jsonData))
	})

	// FilePart marshalling test can be added if needed
}

// Test unmarshalling via container structs (Message/Artifact)
// This specifically tests the custom UnmarshalJSON methods.
func TestPartIfaceUnmarshalJSONCont(t *testing.T) {
	jsonData := `{
		"role": "user",
		"parts": [
			{"kind": "text", "text": "part1"},
			{"kind": "data", "data": {"val": true}},
			{"kind": "text", "text": "part3"}
		]
	}`

	var msg Message
	err := json.Unmarshal([]byte(jsonData), &msg)
	require.NoError(t, err, "Unmarshal into Message should succeed via custom UnmarshalJSON")

	require.Len(t, msg.Parts, 3)
	require.IsType(t, &TextPart{}, msg.Parts[0])
	assert.Equal(t, "part1", msg.Parts[0].(*TextPart).Text)

	require.IsType(t, &DataPart{}, msg.Parts[1])
	assert.Equal(t, map[string]interface{}{"val": true}, msg.Parts[1].(*DataPart).Data)

	require.IsType(t, &TextPart{}, msg.Parts[2])
	assert.Equal(t, "part3", msg.Parts[2].(*TextPart).Text)
}

// Removed TestArtifactPart_MarshalUnmarshalJSON as ArtifactPart type doesn't exist
// and Artifact struct marshalling/unmarshalling is tested implicitly above.

// Removed TestMessage_MarshalUnmarshalJSON as it's covered by TestPartIface_UnmarshalJSON_Container

func TestTaskEvent_IsFinal(t *testing.T) {
	// Use boolPtr helper for pointer boolean values
	tests := []struct {
		name     string
		event    TaskEvent
		expected bool
	}{
		{
			name:     "StatusUpdate Submitted",
			event:    &TaskStatusUpdateEvent{Final: false, Status: TaskStatus{State: TaskStateSubmitted}},
			expected: false,
		},
		{
			name:     "StatusUpdate Working",
			event:    &TaskStatusUpdateEvent{Final: false, Status: TaskStatus{State: TaskStateWorking}},
			expected: false,
		},
		{
			name:     "StatusUpdate Completed",
			event:    &TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateCompleted}},
			expected: true,
		},
		{
			name:     "StatusUpdate Failed",
			event:    &TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateFailed}},
			expected: true,
		},
		{
			name:     "StatusUpdate Canceled",
			event:    &TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateCanceled}},
			expected: true,
		},
		{
			name:     "StatusUpdate Rejected",
			event:    &TaskStatusUpdateEvent{Final: true, Status: TaskStatus{State: TaskStateRejected}},
			expected: true,
		},
		{
			name:     "StatusUpdate AuthRequired",
			event:    &TaskStatusUpdateEvent{Final: false, Status: TaskStatus{State: TaskStateAuthRequired}},
			expected: false,
		},
		{
			name:     "ArtifactUpdate Not Final",
			event:    &TaskArtifactUpdateEvent{},
			expected: false,
		},
		{
			name:     "ArtifactUpdate Final",
			event:    &TaskArtifactUpdateEvent{LastChunk: boolPtr(true)},
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
		{TaskStateRejected, "rejected"},
		{TaskStateAuthRequired, "auth-required"},
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
		Kind: "text",
		Text: "Hello, world!",
	}

	// Create file part
	filePart := NewFilePartWithBytes(
		"example.txt",
		"text/plain",
		"File content",
	)

	// Create a message with these parts
	original := NewMessage(MessageRoleUser, []Part{&textPart, &filePart})

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
	textPartDecoded, ok := decoded.Parts[0].(*TextPart)
	require.True(t, ok, "First part should be a TextPart")
	assert.Equal(t, "text", textPartDecoded.Kind)
	assert.Equal(t, "Hello, world!", textPartDecoded.Text)

	// Check the file part
	filePartDecoded, ok := decoded.Parts[1].(*FilePart)
	require.True(t, ok, "Second part should be a FilePart")
	assert.Equal(t, KindFile, filePartDecoded.Kind)
	fileWithBytes, ok := filePartDecoded.File.(*FileWithBytes)
	assert.True(t, ok, "File part should be a FileWithBytes")
	assert.Equal(t, "example.txt", *fileWithBytes.Name)
	assert.Equal(t, "text/plain", *fileWithBytes.MimeType)
	assert.Equal(t, "File content", fileWithBytes.Bytes)
}

// TestPartValidation tests validation of different part types
func TestPartValidation(t *testing.T) {
	// Test TextPart
	t.Run("TextPart", func(t *testing.T) {
		textPart := NewTextPart("Valid text content")
		assert.True(t, isValidPart(&textPart))

		// Invalid part type
		invalidPart := NewTextPart("")
		assert.False(t, isValidPart(&invalidPart))
	})

	// Test FilePart
	t.Run("FilePart", func(t *testing.T) {
		validFilePart := NewFilePartWithBytes("file.txt", "text/plain", "SGVsbG8=")
		assert.True(t, isValidPart(&validFilePart))

		// Invalid part: missing required file info
		invalidFilePart := NewFilePartWithBytes("file.txt", "text/plain", "")
		assert.False(t, isValidPart(&invalidFilePart))
	})

	// Test DataPart
	t.Run("DataPart", func(t *testing.T) {
		validDataPart := DataPart{
			Kind: KindData,
			Data: map[string]interface{}{"key": "value"},
		}
		assert.True(t, isValidPart(&validDataPart))

		// Invalid part: nil data
		invalidDataPart := DataPart{
			Kind: KindData,
			Data: nil,
		}
		assert.False(t, isValidPart(&invalidDataPart))
	})
}

// isValidPart is a helper function to check if a part is valid
// This is a simplified validation just for testing
func isValidPart(part Part) bool {
	switch p := part.(type) {
	case *TextPart:
		return p.Text != ""
	case *FilePart:
		if fileWithBytes, ok := p.File.(*FileWithBytes); ok {
			return fileWithBytes.Bytes != ""
		}
		if fileWithURI, ok := p.File.(*FileWithURI); ok {
			return fileWithURI.URI != ""
		}
		return false
	case *DataPart:
		return p.Data != nil
	default:
		return false
	}
}

// TestArtifact tests the artifact functionality
func TestArtifact(t *testing.T) {
	// Create a simple text part
	textPart := TextPart{
		Kind: KindText,
		Text: "Artifact content",
	}

	// Create an artifact with generated ID
	artifact := NewArtifactWithID(
		stringPtr("Test Artifact"),
		stringPtr("This is a test artifact"),
		[]Part{&textPart},
	)

	// Validate the artifact
	assert.NotNil(t, artifact.Name)
	assert.Equal(t, "Test Artifact", *artifact.Name)
	assert.NotNil(t, artifact.Description)
	assert.Equal(t, "This is a test artifact", *artifact.Description)
	assert.Len(t, artifact.Parts, 1)
	assert.NotEmpty(t, artifact.ArtifactID)

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
	assert.Equal(t, artifact.ArtifactID, decoded.ArtifactID)

	// Check the part
	require.Len(t, decoded.Parts, 1)
	decodedPart, ok := decoded.Parts[0].(*TextPart)
	require.True(t, ok, "Should decode as a TextPart")
	assert.Equal(t, "text", decodedPart.Kind)
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
	textPart := TextPart{
		Kind: "text",
		Text: "Task completed successfully",
	}
	message := NewMessage(MessageRoleAgent, []Part{&textPart})

	status.Message = &message
	assert.NotNil(t, status.Message)
	assert.Equal(t, MessageRoleAgent, status.Message.Role)
}

// TestMarkerFunctions tests the marker functions for parts and events
func TestMarkerFunctions(t *testing.T) {
	// Test part marker functions
	textPart := TextPart{Kind: "text", Text: "Test"}
	filePart := FilePart{Kind: KindFile}
	dataPart := DataPart{Kind: KindData, Data: map[string]string{"key": "value"}}

	// This test simply ensures the marker functions exist and don't panic
	// They're just marker methods with no behavior
	textPart.partMarker()
	filePart.partMarker()
	dataPart.partMarker()

	// Test event marker functions
	statusEvent := TaskStatusUpdateEvent{TaskID: "test", Status: TaskStatus{State: TaskStateCompleted}}
	artifactEvent := TaskArtifactUpdateEvent{TaskID: "test"}

	// This test simply ensures the marker functions exist and don't panic
	statusEvent.eventMarker()
	artifactEvent.eventMarker()

	// Verify these functions don't have any observable behavior
	// This is just to increase test coverage, as these are marker functions
}

// TestNewTask tests the NewTask factory function
func TestNewTask(t *testing.T) {
	// Test with contextID
	taskID := "test-task"
	contextID := "test-context-123"
	task := NewTask(taskID, contextID)

	assert.Equal(t, taskID, task.ID)
	assert.Equal(t, contextID, task.ContextID)
	assert.Equal(t, TaskStateSubmitted, task.Status.State)
	assert.NotEmpty(t, task.Status.Timestamp)
	assert.NotNil(t, task.Metadata)
	assert.Empty(t, task.Metadata)
	assert.Nil(t, task.Artifacts)
}

// TestGenerateContextID tests the context ID generation function
func TestGenerateContextID(t *testing.T) {
	contextID1 := GenerateContextID()
	assert.NotEmpty(t, contextID1)
	assert.True(t, strings.HasPrefix(contextID1, "ctx-"))
	assert.Len(t, contextID1, 40) // "ctx-" + UUID (36 chars) = 40 chars

	contextID2 := GenerateContextID()
	assert.NotEmpty(t, contextID2)
	assert.NotEqual(t, contextID1, contextID2) // Should be unique
}

// TestGenerateMessageID tests the message ID generation function
func TestGenerateMessageID(t *testing.T) {
	messageID1 := GenerateMessageID()
	assert.NotEmpty(t, messageID1)
	assert.True(t, strings.HasPrefix(messageID1, "msg-"))
	assert.Len(t, messageID1, 40) // "msg-" + UUID (36 chars)

	messageID2 := GenerateMessageID()
	assert.NotEmpty(t, messageID2)
	assert.NotEqual(t, messageID1, messageID2) // Should be unique
}

// TestNewMessage tests the NewMessage factory functions
func TestNewMessage(t *testing.T) {
	// Test basic NewMessage
	textPart := NewTextPart("Hello")
	parts := []Part{&textPart}
	message := NewMessage(MessageRoleUser, parts)

	assert.Equal(t, MessageRoleUser, message.Role)
	assert.Equal(t, parts, message.Parts)
	assert.NotEmpty(t, message.MessageID)
	assert.True(t, strings.HasPrefix(message.MessageID, "msg-"))
	assert.Equal(t, "message", message.Kind)
	assert.Nil(t, message.TaskID)
	assert.Nil(t, message.ContextID)

	// Test NewMessageWithContext
	taskID := "task-123"
	contextID := "ctx-456"
	messageWithContext := NewMessageWithContext(MessageRoleAgent, parts, &taskID, &contextID)

	assert.Equal(t, MessageRoleAgent, messageWithContext.Role)
	assert.Equal(t, parts, messageWithContext.Parts)
	assert.NotEmpty(t, messageWithContext.MessageID)
	assert.Equal(t, "message", messageWithContext.Kind)
	require.NotNil(t, messageWithContext.TaskID)
	assert.Equal(t, taskID, *messageWithContext.TaskID)
	require.NotNil(t, messageWithContext.ContextID)
	assert.Equal(t, contextID, *messageWithContext.ContextID)
}

func TestPartDeserialization(t *testing.T) {
	// Test for TextPart
	t.Run("TextPart", func(t *testing.T) {
		jsonData := `{"kind":"text","text":"Hello","metadata":{"foo":"bar"}}`
		parts, err := unmarshalPartsFromJSON([]byte(fmt.Sprintf("[%s]", jsonData)))
		assert.NoError(t, err)
		assert.Len(t, parts, 1)
		textPartDecoded, ok := parts[0].(*TextPart)
		assert.True(t, ok)
		assert.Equal(t, "text", textPartDecoded.Kind)
		assert.Equal(t, "Hello", textPartDecoded.Text)
		assert.Equal(t, map[string]interface{}{"foo": "bar"}, textPartDecoded.Metadata)
	})

	// Test for FilePart
	t.Run("FilePart", func(t *testing.T) {
		jsonData := `{"kind":"file","file":{"name":"example.txt","mimeType":"text/plain","bytes":"SGVsbG8gV29ybGQ="}}`
		parts, err := unmarshalPartsFromJSON([]byte(fmt.Sprintf("[%s]", jsonData)))
		assert.NoError(t, err)
		assert.Len(t, parts, 1)

		filePartDecoded, ok := parts[0].(*FilePart)
		assert.True(t, ok)
		assert.Equal(t, KindFile, filePartDecoded.Kind)

		// Access FileUnion properly by type assertion
		fileWithBytes, ok := filePartDecoded.File.(*FileWithBytes)
		assert.True(t, ok)
		assert.Equal(t, "example.txt", *fileWithBytes.Name)
		assert.Equal(t, "text/plain", *fileWithBytes.MimeType)
		assert.Equal(t, "SGVsbG8gV29ybGQ=", fileWithBytes.Bytes)
	})

	// Test for DataPart
	t.Run("DataPart", func(t *testing.T) {
		jsonData := `{"kind":"data","data":{"key":"value","number":42}}`
		parts, err := unmarshalPartsFromJSON([]byte(fmt.Sprintf("[%s]", jsonData)))
		assert.NoError(t, err)
		assert.Len(t, parts, 1)

		dataPartDecoded, ok := parts[0].(*DataPart)
		assert.True(t, ok)
		assert.Equal(t, KindData, dataPartDecoded.Kind)

		// Access the data as a map
		dataMap, ok := dataPartDecoded.Data.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "value", dataMap["key"])
		assert.Equal(t, float64(42), dataMap["number"]) // JSON numbers are float64
	})
}

func TestMessage_MarshalJSON(t *testing.T) {
	textPart := TextPart{
		Kind: "text",
		Text: "Hello",
	}

	message := Message{
		Role:  MessageRoleUser,
		Parts: []Part{&textPart},
	}

	jsonData, err := json.Marshal(message)
	require.NoError(t, err)

	var decodedMessage Message
	err = json.Unmarshal(jsonData, &decodedMessage)
	require.NoError(t, err)

	// Verify that the unmarshaled Part is a concrete TextPart
	require.Len(t, decodedMessage.Parts, 1)
	textPartFromDecoded, ok := decodedMessage.Parts[0].(*TextPart)
	require.True(t, ok)
	assert.Equal(t, "text", textPartFromDecoded.Kind)
	assert.Equal(t, "Hello", textPartFromDecoded.Text)
}

func TestMessage_UnmarshalJSON(t *testing.T) {
	jsonData := `{
	"role": "user",
	"parts": [
		{"kind": "text", "text": "Hello"},
		{"kind": "file", "file": {"name": "test.txt", "mimeType": "text/plain", "bytes": "SGVsbG8="}}
	]
}`

	var message Message
	err := json.Unmarshal([]byte(jsonData), &message)
	require.NoError(t, err)

	assert.Equal(t, MessageRoleUser, message.Role)
	require.Len(t, message.Parts, 2)

	// Check first part (TextPart)
	textPart, ok := message.Parts[0].(*TextPart)
	assert.True(t, ok)
	assert.Equal(t, "text", textPart.Kind)
	assert.Equal(t, "Hello", textPart.Text)

	// Check second part (FilePart)
	filePart, ok := message.Parts[1].(*FilePart)
	assert.True(t, ok)
	assert.Equal(t, KindFile, filePart.Kind)

	// Access FileUnion properly by type assertion
	fileWithBytes, ok := filePart.File.(*FileWithBytes)
	assert.True(t, ok)
	assert.Equal(t, "test.txt", *fileWithBytes.Name)
}

// Helper function to unmarshal JSON array of parts
func unmarshalPartsFromJSON(data []byte) ([]Part, error) {
	var rawParts []json.RawMessage
	if err := json.Unmarshal(data, &rawParts); err != nil {
		return nil, err
	}

	parts := make([]Part, len(rawParts))
	for i, rawPart := range rawParts {
		part, err := unmarshalPart(rawPart)
		if err != nil {
			return nil, err
		}
		parts[i] = part
	}
	return parts, nil
}
