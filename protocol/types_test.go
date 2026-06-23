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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }

func TestTaskState(t *testing.T) {
	tests := []struct {
		state    TaskState
		expected string
	}{
		{TaskStateSubmitted, "TASK_STATE_SUBMITTED"},
		{TaskStateWorking, "TASK_STATE_WORKING"},
		{TaskStateCompleted, "TASK_STATE_COMPLETED"},
		{TaskStateCanceled, "TASK_STATE_CANCELED"},
		{TaskStateFailed, "TASK_STATE_FAILED"},
		{TaskStateRejected, "TASK_STATE_REJECTED"},
		{TaskStateAuthRequired, "TASK_STATE_AUTH_REQUIRED"},
		{TaskStateUnspecified, "TASK_STATE_UNSPECIFIED"},
		{TaskStateInputRequired, "TASK_STATE_INPUT_REQUIRED"},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			assert.Equal(t, tc.expected, string(tc.state))
		})
	}
}

func TestMessageRole(t *testing.T) {
	assert.Equal(t, "ROLE_USER", string(MessageRoleUser))
	assert.Equal(t, "ROLE_AGENT", string(MessageRoleAgent))
}

func TestMessageJSON(t *testing.T) {
	msg := NewMessage(MessageRoleUser, []*Part{
		NewTextPart("Hello, world!"),
		NewURLPart("https://example.com/f.txt", "text/plain"),
	})

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, MessageRoleUser, decoded.Role)
	require.Len(t, decoded.Parts, 2)
	assert.Equal(t, "Hello, world!", decoded.Parts[0].TextContent())
	assert.Equal(t, "https://example.com/f.txt", decoded.Parts[1].URLContent())
}

func TestArtifactJSON(t *testing.T) {
	artifact := NewArtifactWithID(
		stringPtr("Test Artifact"),
		stringPtr("description"),
		[]*Part{NewTextPart("Artifact content")},
	)

	data, err := json.Marshal(artifact)
	require.NoError(t, err)

	var decoded Artifact
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *artifact.Name, *decoded.Name)
	assert.Equal(t, artifact.ArtifactID, decoded.ArtifactID)
	require.Len(t, decoded.Parts, 1)
	assert.Equal(t, "Artifact content", decoded.Parts[0].TextContent())
}

func TestTaskEventIsFinal(t *testing.T) {
	tests := []struct {
		name  string
		final bool
		event interface{ IsFinal() bool }
	}{
		{"StatusUpdate non-final", false, &TaskStatusUpdateEvent{Final: false}},
		{"StatusUpdate final", true, &TaskStatusUpdateEvent{Final: true}},
		{"ArtifactUpdate not final", false, &TaskArtifactUpdateEvent{}},
		{"ArtifactUpdate final", true, &TaskArtifactUpdateEvent{LastChunk: boolPtr(true)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.final, tc.event.IsFinal())
		})
	}
}

func TestNewTask(t *testing.T) {
	task := NewTask("test-task", "test-context")
	assert.Equal(t, "test-task", task.ID)
	assert.Equal(t, "test-context", task.ContextID)
	assert.Equal(t, TaskStateSubmitted, task.Status.State)
	assert.NotEmpty(t, task.Status.Timestamp)
	assert.NotNil(t, task.Metadata)
}

func TestTaskStatus(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	status := TaskStatus{State: TaskStateCompleted, Timestamp: now}
	assert.Equal(t, TaskStateCompleted, status.State)

	msg := NewMessage(MessageRoleAgent, []*Part{NewTextPart("done")})
	status.Message = &msg
	assert.NotNil(t, status.Message)
}

func TestGenerateIDs(t *testing.T) {
	assert.True(t, strings.HasPrefix(GenerateContextID(), "ctx-"))
	assert.True(t, strings.HasPrefix(GenerateMessageID(), "msg-"))
	assert.True(t, strings.HasPrefix(GenerateTaskID(), "task-"))
	assert.True(t, strings.HasPrefix(GenerateArtifactID(), "artifact-"))
	assert.NotEmpty(t, GenerateRPCID())

	// Uniqueness
	assert.NotEqual(t, GenerateMessageID(), GenerateMessageID())
}

func TestNewMessage(t *testing.T) {
	parts := []*Part{NewTextPart("Hello")}
	msg := NewMessage(MessageRoleUser, parts)

	assert.Equal(t, MessageRoleUser, msg.Role)
	assert.Len(t, msg.Parts, 1)
	assert.True(t, strings.HasPrefix(msg.MessageID, "msg-"))

	taskID := "task-123"
	contextID := "ctx-456"
	msgCtx := NewMessageWithContext(MessageRoleAgent, parts, &taskID, &contextID)
	assert.Equal(t, MessageRoleAgent, msgCtx.Role)
	require.NotNil(t, msgCtx.TaskID)
	assert.Equal(t, taskID, *msgCtx.TaskID)
}

func TestSendMessageResponseJSON(t *testing.T) {
	t.Run("with message", func(t *testing.T) {
		msg := NewMessage(MessageRoleAgent, []*Part{NewTextPart("hello")})
		resp := NewSendMessageResponseMessage(&msg)
		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded SendMessageResponse
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		require.NotNil(t, decoded.GetMessage())
		assert.Nil(t, decoded.GetTask())
		assert.Equal(t, "hello", decoded.GetMessage().Parts[0].TextContent())
	})

	t.Run("with task", func(t *testing.T) {
		task := NewTask("t1", "c1")
		resp := NewSendMessageResponseTask(task)
		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded SendMessageResponse
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Nil(t, decoded.GetMessage())
		require.NotNil(t, decoded.GetTask())
		assert.Equal(t, "t1", decoded.GetTask().ID)
	})
}

func TestStreamResponseJSON(t *testing.T) {
	t.Run("statusUpdate", func(t *testing.T) {
		e := &TaskStatusUpdateEvent{
			TaskID: "t1",
			Status: TaskStatus{State: TaskStateWorking},
			Final:  false,
		}
		resp := NewStreamResponseStatusUpdate(e)
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"statusUpdate"`)

		var decoded StreamResponse
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		require.NotNil(t, decoded.GetStatusUpdate())
		assert.Equal(t, "t1", decoded.GetStatusUpdate().TaskID)
	})

	t.Run("artifactUpdate", func(t *testing.T) {
		e := &TaskArtifactUpdateEvent{
			TaskID:   "t2",
			Artifact: Artifact{ArtifactID: "a1", Parts: []*Part{NewTextPart("data")}},
		}
		resp := NewStreamResponseArtifactUpdate(e)
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"artifactUpdate"`)

		var decoded StreamResponse
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		require.NotNil(t, decoded.GetArtifactUpdate())
		assert.Equal(t, "a1", decoded.GetArtifactUpdate().Artifact.ArtifactID)
	})

	t.Run("EventType", func(t *testing.T) {
		assert.Equal(t, EventStatusUpdate, (&StreamResponse{Result: &TaskStatusUpdateEvent{}}).EventType())
		assert.Equal(t, EventArtifactUpdate, (&StreamResponse{Result: &TaskArtifactUpdateEvent{}}).EventType())
		assert.Equal(t, EventTask, (&StreamResponse{Result: &Task{}}).EventType())
		assert.Equal(t, EventMessage, (&StreamResponse{Result: &Message{}}).EventType())
		assert.Equal(t, "", (&StreamResponse{}).EventType())
	})
}

func TestSendMessageConfigurationIsBlocking(t *testing.T) {
	// v1.0: returnImmediately defaults to false, so an unset config (or unset
	// ReturnImmediately) blocks until a terminal/interrupted state.
	assert.True(t, (*SendMessageConfiguration)(nil).IsBlocking())
	assert.True(t, (&SendMessageConfiguration{}).IsBlocking())
	assert.False(t, (&SendMessageConfiguration{ReturnImmediately: boolPtr(true)}).IsBlocking())
	assert.True(t, (&SendMessageConfiguration{ReturnImmediately: boolPtr(false)}).IsBlocking())
}
