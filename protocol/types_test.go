// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "trpc.group/trpc-go/trpc-a2a-go/protocol/a2apb"
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
		payload := map[string]any{"a": 1}
		d, _ := structpb.NewStruct(payload)
		part := DataPart{Data: &v1.DataPart{
			Data: d,
		}}
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

	require.Len(t, msg.Content, 3)
	require.IsType(t, &TextPart{}, msg.Content[0])
	assert.Equal(t, "part1", msg.Content[0].Part.(*TextPart).Text)

	require.IsType(t, &DataPart{}, msg.Content[1])
	assert.Equal(t, map[string]interface{}{"val": true}, msg.Content[1].Part.(*DataPart).Data)

	require.IsType(t, &TextPart{}, msg.Content[2])
	assert.Equal(t, "part3", msg.Content[2].Part.(*TextPart).Text)
}

// Removed TestArtifactPart_MarshalUnmarshalJSON as ArtifactPart type doesn't exist
// and Artifact struct marshalling/unmarshalling is tested implicitly above.

// Removed TestMessage_MarshalUnmarshalJSON as it's covered by TestPartIface_UnmarshalJSON_Container

func TestTaskEvent_IsFinal(t *testing.T) {
	// Use NewTaskStatusUpdateEvent constructor for proper creation
	tests := []struct {
		name     string
		event    StreamingMessageResult
		expected bool
	}{
		{
			name:     "StatusUpdate Submitted",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: false, Status: &v1.TaskStatus{State: TaskStateSubmitted}}},
			expected: false,
		},
		{
			name:     "StatusUpdate Working",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: false, Status: &v1.TaskStatus{State: TaskStateWorking}}},
			expected: false,
		},
		{
			name:     "StatusUpdate Completed",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: true, Status: &v1.TaskStatus{State: TaskStateCompleted}}},
			expected: true,
		},
		{
			name:     "StatusUpdate Failed",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: true, Status: &v1.TaskStatus{State: TaskStateFailed}}},
			expected: true,
		},
		{
			name:     "StatusUpdate Canceled",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: true, Status: &v1.TaskStatus{State: TaskStateCanceled}}},
			expected: true,
		},
		{
			name:     "StatusUpdate Rejected",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: true, Status: &v1.TaskStatus{State: TaskStateRejected}}},
			expected: true,
		},
		{
			name:     "StatusUpdate AuthRequired",
			event:    &TaskStatusUpdateEvent{TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{Final: false, Status: &v1.TaskStatus{State: TaskStateAuthRequired}}},
			expected: false,
		},
		{
			name:     "ArtifactUpdate Not Final",
			event:    &TaskArtifactUpdateEvent{TaskArtifactUpdateEvent: &v1.TaskArtifactUpdateEvent{LastChunk: false}},
			expected: false,
		},
		{
			name:     "ArtifactUpdate Final",
			event:    &TaskArtifactUpdateEvent{TaskArtifactUpdateEvent: &v1.TaskArtifactUpdateEvent{LastChunk: true}},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if event, ok := tc.event.(*TaskArtifactUpdateEvent); ok {
				assert.Equal(t, tc.expected, event.IsFinal())
				return
			}

			if event, ok := tc.event.(*TaskStatusUpdateEvent); ok {
				assert.Equal(t, tc.expected, event.IsFinal())
				return
			}

			t.Fatal("unexpected event type")

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
	// Create text part using v1.Part structure
	textPart := &v1.Part{
		Part: &v1.Part_Text{Text: "Hello, world!"},
	}

	// Create file part
	filePart := &v1.Part{
		Part: &v1.Part_File{
			File: &v1.FilePart{
				File: &v1.FilePart_FileWithBytes{
					FileWithBytes: []byte("File content"),
				},
			},
		},
	}

	// Create a message with these parts
	original := NewMessage(MessageRoleUser, []*v1.Part{textPart, filePart})

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
	require.Len(t, decoded.Content, 2)

	// Check the text part
	textPartDecoded := decoded.Content[0].Part.(*v1.Part_Text)
	assert.Equal(t, "Hello, world!", textPartDecoded.Text)

	// Check the file part
	filePartDecoded := decoded.Content[1].Part.(*v1.Part_File)
	fileWithBytes := filePartDecoded.File.File.(*v1.FilePart_FileWithBytes)
	assert.Equal(t, "File content", string(fileWithBytes.FileWithBytes))
}

// TestPartValidation tests validation of different part types
func TestPartValidation(t *testing.T) {
	// Test TextPart
	t.Run("TextPart", func(t *testing.T) {
		textPart := &v1.Part{
			Part: &v1.Part_Text{Text: "Valid text content"},
		}
		assert.True(t, isValidPart(textPart))

		// Invalid part type
		invalidPart := &v1.Part{
			Part: &v1.Part_Text{Text: ""},
		}
		assert.False(t, isValidPart(invalidPart))
	})

	// Test FilePart
	t.Run("FilePart", func(t *testing.T) {
		validFilePart := &v1.Part{
			Part: &v1.Part_File{
				File: &v1.FilePart{
					File: &v1.FilePart_FileWithBytes{
						FileWithBytes: []byte("SGVsbG8="),
					},
				},
			},
		}
		assert.True(t, isValidPart(validFilePart))

		// Invalid part: missing required file info
		invalidFilePart := &v1.Part{
			Part: &v1.Part_File{
				File: &v1.FilePart{
					File: &v1.FilePart_FileWithBytes{
						FileWithBytes: []byte(""),
					},
				},
			},
		}
		assert.False(t, isValidPart(invalidFilePart))
	})

	// Test DataPart
	t.Run("DataPart", func(t *testing.T) {
		data, _ := structpb.NewStruct(map[string]interface{}{"key": "value"})
		validDataPart := &v1.Part{
			Part: &v1.Part_Data{
				Data: &v1.DataPart{
					Data: data,
				},
			},
		}
		assert.True(t, isValidPart(validDataPart))

		// Invalid part: nil data
		invalidDataPart := &v1.Part{
			Part: &v1.Part_Data{
				Data: &v1.DataPart{
					Data: nil,
				},
			},
		}
		assert.False(t, isValidPart(invalidDataPart))
	})
}

// isValidPart is a helper function to check if a part is valid
// This is a simplified validation just for testing
func isValidPart(part *v1.Part) bool {
	switch p := part.Part.(type) {
	case *v1.Part_Text:
		return p.Text != ""
	case *v1.Part_File:
		if fileWithBytes, ok := p.File.File.(*v1.FilePart_FileWithBytes); ok {
			return len(fileWithBytes.FileWithBytes) > 0
		}
		if fileWithURI, ok := p.File.File.(*v1.FilePart_FileWithUri); ok {
			return fileWithURI.FileWithUri != ""
		}
		return false
	case *v1.Part_Data:
		return p.Data.Data != nil
	default:
		return false
	}
}

// TestArtifact tests the artifact functionality
func TestArtifact(t *testing.T) {
	// Create a simple text part
	textPart := &v1.Part{
		Part: &v1.Part_Text{Text: "Artifact content"},
	}

	// Create an artifact with generated ID
	artifact := NewArtifactWithID(
		stringPtr("Test Artifact"),
		stringPtr("This is a test artifact"),
		[]*v1.Part{textPart},
	)

	// Validate the artifact
	assert.NotNil(t, artifact.Name)
	assert.Equal(t, "Test Artifact", artifact.Name)
	assert.NotNil(t, artifact.Description)
	assert.Equal(t, "This is a test artifact", artifact.Description)
	assert.Len(t, artifact.Parts, 1)
	assert.NotEmpty(t, artifact.ArtifactId)

	// Test JSON marshaling
	data, err := json.Marshal(artifact)
	require.NoError(t, err)

	// Test JSON unmarshaling
	var decoded Artifact
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify the decoded artifact
	assert.Equal(t, artifact.Name, decoded.Name)
	assert.Equal(t, artifact.Description, decoded.Description)
	assert.Equal(t, artifact.ArtifactId, decoded.ArtifactId)

	// Check the part
	require.Len(t, decoded.Parts, 1)
	decodedPart := decoded.Parts[0].Part.(*v1.Part_Text)
	assert.Equal(t, "Artifact content", decodedPart.Text)
}

// TestTaskStatus tests the TaskStatus type
func TestTaskStatus(t *testing.T) {
	now := timestamppb.Now()

	// Create a task status
	status := &v1.TaskStatus{
		State:     TaskStateCompleted,
		Timestamp: now,
	}

	// Validate the fields
	assert.Equal(t, TaskStateCompleted, status.State)
	assert.Equal(t, now, status.Timestamp)

	// TaskStatus doesn't have a Message field in the protobuf definition
	// so we'll just test the basic fields
}

// TestMarkerFunctions tests the marker functions for parts and events
func TestMarkerFunctions(t *testing.T) {
	// Test event marker functions
	statusEvent := TaskStatusUpdateEvent{
		TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{
			TaskId: "test",
			Status: &v1.TaskStatus{State: TaskStateCompleted},
		},
	}
	artifactEvent := TaskArtifactUpdateEvent{
		TaskArtifactUpdateEvent: &v1.TaskArtifactUpdateEvent{
			TaskId: "test",
		},
	}

	// This test simply ensures the marker functions exist and don't panic
	statusEvent.streamingMessageResultMarker()
	artifactEvent.streamingMessageResultMarker()

	// Verify these functions don't have any observable behavior
	// This is just to increase test coverage, as these are marker functions
}

// TestNewTask tests the NewTask factory function
func TestNewTask(t *testing.T) {
	// Test with contextID
	taskID := "test-task"
	contextID := "test-context-123"
	task := NewTask(taskID, contextID)

	assert.Equal(t, taskID, task.Id)
	assert.Equal(t, contextID, task.ContextId)
	assert.Equal(t, TaskStateSubmitted, task.Status.State)
	assert.NotNil(t, task.Status.Timestamp)
	assert.NotNil(t, task.Metadata)
	assert.Empty(t, task.Metadata.Fields)
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
	textPart := &v1.Part{
		Part: &v1.Part_Text{Text: "Hello"},
	}
	parts := []*v1.Part{textPart}
	message := NewMessage(MessageRoleUser, parts)

	assert.Equal(t, MessageRoleUser, message.Role)
	assert.Equal(t, parts, message.Content)
	assert.NotEmpty(t, message.MessageId)
	assert.True(t, strings.HasPrefix(message.MessageId, "msg-"))
	assert.Equal(t, "message", message.GetKind())
	assert.Empty(t, message.TaskId)
	assert.Empty(t, message.ContextId)

	// Test NewMessageWithContext
	taskID := "task-123"
	contextID := "ctx-456"
	messageWithContext := NewMessageWithContext(MessageRoleAgent, parts, &taskID, &contextID)

	assert.Equal(t, MessageRoleAgent, messageWithContext.Role)
	assert.Equal(t, parts, messageWithContext.Content)
	assert.NotEmpty(t, messageWithContext.MessageId)
	assert.Equal(t, "message", messageWithContext.GetKind())
	assert.Equal(t, taskID, messageWithContext.TaskId)
	assert.Equal(t, contextID, messageWithContext.ContextId)
}

func TestPartDeserialization(t *testing.T) {
	// Test for TextPart
	t.Run("TextPart", func(t *testing.T) {
		jsonData := `{"text":"Hello"}`
		var part v1.Part
		err := json.Unmarshal([]byte(jsonData), &part)
		assert.NoError(t, err)

		textPart := part.Part.(*v1.Part_Text)
		assert.Equal(t, "Hello", textPart.Text)
	})

	// Test for FilePart
	t.Run("FilePart", func(t *testing.T) {
		jsonData := `{"file":{"fileWithBytes":"SGVsbG8gV29ybGQ="}}`
		var part v1.Part
		err := json.Unmarshal([]byte(jsonData), &part)
		assert.NoError(t, err)

		filePart := part.Part.(*v1.Part_File)
		fileWithBytes := filePart.File.File.(*v1.FilePart_FileWithBytes)
		assert.Equal(t, "SGVsbG8gV29ybGQ=", string(fileWithBytes.FileWithBytes))
	})

	// Test for DataPart
	t.Run("DataPart", func(t *testing.T) {
		jsonData := `{"data":{"data":{"key":"value","number":42}}}`
		var part v1.Part
		err := json.Unmarshal([]byte(jsonData), &part)
		assert.NoError(t, err)

		dataPart := part.Part.(*v1.Part_Data)
		dataMap := dataPart.Data.Data.AsMap()
		assert.Equal(t, "value", dataMap["key"])
		assert.Equal(t, float64(42), dataMap["number"]) // JSON numbers are float64
	})
}

func TestMessage_MarshalJSON(t *testing.T) {
	textPart := &v1.Part{
		Part: &v1.Part_Text{Text: "Hello"},
	}

	message := Message{
		Message: &v1.Message{
			Role:    MessageRoleUser,
			Content: []*v1.Part{textPart},
		},
	}

	jsonData, err := json.Marshal(message)
	require.NoError(t, err)

	var decodedMessage Message
	err = json.Unmarshal(jsonData, &decodedMessage)
	require.NoError(t, err)

	// Verify that the unmarshaled Part is a concrete TextPart
	require.Len(t, decodedMessage.Content, 1)
	textPartFromDecoded := decodedMessage.Content[0].Part.(*v1.Part_Text)
	assert.Equal(t, "Hello", textPartFromDecoded.Text)
}

func TestMessage_UnmarshalJSON(t *testing.T) {
	jsonData := `{
	"role": "user",
	"content": [
		{"text": "Hello"},
		{"file": {"fileWithBytes": "SGVsbG8="}}
	]
}`

	var message Message
	err := json.Unmarshal([]byte(jsonData), &message)
	require.NoError(t, err)

	assert.Equal(t, MessageRoleUser, message.Role)
	require.Len(t, message.Content, 2)

	// Check first part (TextPart)
	textPart := message.Content[0].Part.(*v1.Part_Text)
	assert.Equal(t, "Hello", textPart.Text)

	// Check second part (FilePart)
	filePart := message.Content[1].Part.(*v1.Part_File)
	fileWithBytes := filePart.File.File.(*v1.FilePart_FileWithBytes)
	assert.Equal(t, "SGVsbG8=", string(fileWithBytes.FileWithBytes))
}
