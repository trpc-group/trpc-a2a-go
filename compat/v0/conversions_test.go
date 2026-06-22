// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestTaskStateRoundTrip(t *testing.T) {
	states := []TaskState{
		TaskStateSubmitted, TaskStateWorking, TaskStateInputRequired,
		TaskStateCompleted, TaskStateCanceled, TaskStateFailed,
		TaskStateRejected, TaskStateAuthRequired,
	}
	for _, s := range states {
		assert.Equal(t, s, FromV1TaskState(ToV1TaskState(s)), "round-trip for %s", s)
	}
	// Wire values are translated, not passed through.
	assert.Equal(t, protocol.TaskStateSubmitted, ToV1TaskState(TaskStateSubmitted))
	assert.Equal(t, "TASK_STATE_SUBMITTED", string(ToV1TaskState("submitted")))
	// Alternative legacy spelling.
	assert.Equal(t, protocol.TaskStateCanceled, ToV1TaskState("cancelled"))
	// Unknown maps to unspecified and back.
	assert.Equal(t, protocol.TaskStateUnspecified, ToV1TaskState(TaskStateUnknown))
	assert.Equal(t, TaskStateUnknown, FromV1TaskState(protocol.TaskStateUnspecified))
}

func TestRoleRoundTrip(t *testing.T) {
	assert.Equal(t, protocol.MessageRoleUser, ToV1Role(MessageRoleUser))
	assert.Equal(t, protocol.MessageRoleAgent, ToV1Role(MessageRoleAgent))
	assert.Equal(t, MessageRoleUser, FromV1Role(protocol.MessageRoleUser))
	assert.Equal(t, MessageRoleAgent, FromV1Role(protocol.MessageRoleAgent))
}

func TestPartRoundTrip(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		v1 := ToV1Part(TextPart{Kind: KindText, Text: "hello"})
		assert.Equal(t, "hello", v1.TextContent())
		back := FromV1Part(v1)
		tp, ok := back.(TextPart)
		require.True(t, ok, "expected TextPart, got %T", back)
		assert.Equal(t, "hello", tp.Text)
		assert.Equal(t, KindText, tp.Kind)
	})

	t.Run("data non-map value", func(t *testing.T) {
		// v0 DataPart.Data is interface{}, so arrays survive without wrappers.
		v1 := ToV1Part(DataPart{Kind: KindData, Data: []interface{}{1.0, 2.0}})
		back := FromV1Part(v1)
		dp, ok := back.(DataPart)
		require.True(t, ok)
		assert.Equal(t, []interface{}{1.0, 2.0}, dp.Data)
	})

	t.Run("file bytes", func(t *testing.T) {
		name, mime := "doc.pdf", "application/pdf"
		legacy := FilePart{
			Kind: KindFile,
			File: &FileWithBytes{Name: &name, MimeType: &mime, Bytes: "aGVsbG8="}, // "hello"
		}
		v1 := ToV1Part(legacy)
		assert.Equal(t, []byte("hello"), v1.RawContent())
		assert.Equal(t, "doc.pdf", v1.Filename)
		assert.Equal(t, "application/pdf", v1.MediaType)

		back := FromV1Part(v1)
		fp, ok := back.(FilePart)
		require.True(t, ok)
		fb, ok := fp.File.(*FileWithBytes)
		require.True(t, ok)
		assert.Equal(t, "aGVsbG8=", fb.Bytes)
		assert.Equal(t, "doc.pdf", *fb.Name)
		assert.Equal(t, "application/pdf", *fb.MimeType)
	})

	t.Run("file uri", func(t *testing.T) {
		mime := "image/png"
		legacy := FilePart{
			Kind: KindFile,
			File: &FileWithURI{MimeType: &mime, URI: "https://example.com/x.png"},
		}
		v1 := ToV1Part(legacy)
		assert.Equal(t, "https://example.com/x.png", v1.URLContent())

		back := FromV1Part(v1)
		fp, ok := back.(FilePart)
		require.True(t, ok)
		fu, ok := fp.File.(*FileWithURI)
		require.True(t, ok)
		assert.Equal(t, "https://example.com/x.png", fu.URI)
		assert.Nil(t, fu.Name)
		assert.Equal(t, "image/png", *fu.MimeType)
	})
}

func TestMessageRoundTrip(t *testing.T) {
	ctxID, taskID := "ctx-1", "task-1"
	legacy := Message{
		Kind:      KindMessage,
		MessageID: "msg-1",
		Role:      MessageRoleUser,
		ContextID: &ctxID,
		TaskID:    &taskID,
		Parts:     []Part{TextPart{Kind: KindText, Text: "hi"}},
		Metadata:  map[string]interface{}{"k": "v"},
	}
	v1 := ToV1Message(&legacy)
	require.NotNil(t, v1)
	assert.Equal(t, protocol.MessageRoleUser, v1.Role)
	assert.Equal(t, "msg-1", v1.MessageID)

	back := FromV1Message(v1)
	require.NotNil(t, back)
	assert.Equal(t, KindMessage, back.Kind)
	assert.Equal(t, legacy.MessageID, back.MessageID)
	assert.Equal(t, legacy.Role, back.Role)
	assert.Equal(t, *legacy.ContextID, *back.ContextID)
	assert.Equal(t, *legacy.TaskID, *back.TaskID)
	assert.Equal(t, legacy.Metadata, back.Metadata)
	require.Len(t, back.Parts, 1)
	assert.Equal(t, "hi", back.Parts[0].(TextPart).Text)
}

func TestStatusUpdateFinalDerivation(t *testing.T) {
	// A terminal v1 event without an explicit Final flag must surface
	// final=true on the legacy wire.
	v1Event := &protocol.TaskStatusUpdateEvent{
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status:    protocol.TaskStatus{State: protocol.TaskStateCompleted},
	}
	legacy := FromV1StatusUpdate(v1Event)
	assert.True(t, legacy.Final, "terminal state must derive final=true")
	assert.Equal(t, KindTaskStatusUpdate, legacy.Kind)
	assert.Equal(t, TaskStateCompleted, legacy.Status.State)

	// input-required also terminates a legacy stream.
	v1Event.Status.State = protocol.TaskStateInputRequired
	assert.True(t, FromV1StatusUpdate(v1Event).Final)

	// working is not final.
	v1Event.Status.State = protocol.TaskStateWorking
	assert.False(t, FromV1StatusUpdate(v1Event).Final)
}

func TestSendMessageParamsBlockingMapping(t *testing.T) {
	blockingTrue := true
	legacy := SendMessageParams{
		Message: Message{Kind: KindMessage, MessageID: "m1", Role: MessageRoleUser,
			Parts: []Part{TextPart{Kind: KindText, Text: "x"}}},
		Configuration: &SendMessageConfiguration{Blocking: &blockingTrue},
	}
	v1 := ToV1SendMessageParams(legacy)
	require.NotNil(t, v1.Configuration)
	require.NotNil(t, v1.Configuration.ReturnImmediately)
	assert.False(t, *v1.Configuration.ReturnImmediately, "blocking=true -> returnImmediately=false")
	assert.True(t, v1.Configuration.IsBlocking())

	// Unset blocking (v0 default non-blocking) -> returnImmediately=true.
	legacy.Configuration.Blocking = nil
	v1 = ToV1SendMessageParams(legacy)
	assert.True(t, *v1.Configuration.ReturnImmediately)

	// Reverse direction.
	back := FromV1SendMessageParams(v1)
	require.NotNil(t, back.Configuration.Blocking)
	assert.False(t, *back.Configuration.Blocking)
}

func TestPushConfigRoundTrip(t *testing.T) {
	creds := "secret"
	legacy := TaskPushNotificationConfig{
		TaskID: "task-1",
		PushNotificationConfig: PushNotificationConfig{
			ID:    "cfg-1",
			URL:   "https://example.com/webhook",
			Token: "tok",
			Authentication: &PushNotificationAuthenticationInfo{
				Schemes:     []string{"Bearer"},
				Credentials: &creds,
			},
		},
	}
	v1 := ToV1PushConfig(legacy)
	assert.Equal(t, "task-1", v1.TaskID)
	assert.Equal(t, "cfg-1", v1.ID)
	assert.Equal(t, "https://example.com/webhook", v1.URL)
	require.NotNil(t, v1.Authentication)
	assert.Equal(t, "Bearer", v1.Authentication.Scheme)
	assert.Equal(t, "secret", v1.Authentication.Credentials)

	back := FromV1PushConfig(v1)
	assert.Equal(t, legacy.TaskID, back.TaskID)
	assert.Equal(t, legacy.PushNotificationConfig.ID, back.PushNotificationConfig.ID)
	assert.Equal(t, legacy.PushNotificationConfig.URL, back.PushNotificationConfig.URL)
	assert.Equal(t, legacy.PushNotificationConfig.Token, back.PushNotificationConfig.Token)
	require.NotNil(t, back.PushNotificationConfig.Authentication)
	assert.Equal(t, []string{"Bearer"}, back.PushNotificationConfig.Authentication.Schemes)
	assert.Equal(t, "secret", *back.PushNotificationConfig.Authentication.Credentials)
}

func TestTaskRoundTrip(t *testing.T) {
	name := "result"
	legacy := &Task{
		ID:        "task-1",
		ContextID: "ctx-1",
		Kind:      KindTask,
		Status: TaskStatus{
			State:     TaskStateCompleted,
			Timestamp: "2026-01-01T00:00:00Z",
		},
		Artifacts: []Artifact{{
			ArtifactID: "art-1",
			Name:       &name,
			Parts:      []Part{TextPart{Kind: KindText, Text: "out"}},
		}},
		History: []Message{{Kind: KindMessage, MessageID: "m1", Role: MessageRoleUser,
			Parts: []Part{TextPart{Kind: KindText, Text: "in"}}}},
	}
	v1 := ToV1Task(legacy)
	assert.Equal(t, protocol.TaskStateCompleted, v1.Status.State)
	require.Len(t, v1.Artifacts, 1)
	require.Len(t, v1.History, 1)

	back := FromV1Task(v1)
	assert.Equal(t, KindTask, back.Kind)
	assert.Equal(t, legacy.ID, back.ID)
	assert.Equal(t, legacy.Status.State, back.Status.State)
	assert.Equal(t, legacy.Status.Timestamp, back.Status.Timestamp)
	require.Len(t, back.Artifacts, 1)
	assert.Equal(t, "art-1", back.Artifacts[0].ArtifactID)
	assert.Equal(t, "result", *back.Artifacts[0].Name)
	require.Len(t, back.History, 1)
	assert.Equal(t, "m1", back.History[0].MessageID)
}

func TestFillLegacyCardFields(t *testing.T) {
	enabled := true
	card := protocol.AgentCard{
		Name: "agent",
		SupportedInterfaces: []protocol.AgentInterface{
			{URL: "https://a.example.com", ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0"},
			{URL: "https://b.example.com", ProtocolBinding: "GRPC", ProtocolVersion: "1.0"},
		},
		Capabilities: protocol.AgentCapabilities{ExtendedAgentCard: &enabled},
	}
	FillLegacyCardFields(&card)

	assert.Equal(t, "https://a.example.com", card.URL)
	require.NotNil(t, card.PreferredTransport)
	assert.Equal(t, "JSONRPC", *card.PreferredTransport)
	require.NotNil(t, card.ProtocolVersion)
	assert.Equal(t, Version, *card.ProtocolVersion)
	require.Len(t, card.AdditionalInterfaces, 1)
	assert.Equal(t, "https://b.example.com", card.AdditionalInterfaces[0].URL)
	require.NotNil(t, card.SupportsAuthenticatedExtendedCard)
	assert.True(t, *card.SupportsAuthenticatedExtendedCard)
}
