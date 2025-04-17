// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager handles the lifecycle and state of A2A tasks,
// defining the core types and interfaces based on the A2A specification.
package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// TaskState represents the lifecycle state of a task.
// See A2A Spec section on Task Lifecycle.
type TaskState string

// TaskState constants define the possible states of a task.
const (
	// TaskStateSubmitted is the state when the task is received but not yet processed.
	TaskStateSubmitted TaskState = "submitted"
	// TaskStateWorking is the state when the task is actively being processed.
	TaskStateWorking TaskState = "working"
	// TaskStateInputRequired is the state when the task requires additional input (semantics evolving in spec).
	TaskStateInputRequired TaskState = "input-required"
	// TaskStateCompleted is the state when the task finished successfully.
	TaskStateCompleted TaskState = "completed"
	// TaskStateCanceled is the state when the task was canceled before completion.
	TaskStateCanceled TaskState = "canceled"
	// TaskStateFailed is the state when the task failed during processing.
	TaskStateFailed TaskState = "failed"
	// TaskStateUnknown is the state when the task is in an unknown or indeterminate state.
	TaskStateUnknown TaskState = "unknown"
)

// MessageRole indicates the originator of a message (user or agent).
// See A2A Spec section on Messages.
type MessageRole string

// MessageRole constants define the possible roles for a message sender.
const (
	// MessageRoleUser is the role of a message originated from the user/client.
	MessageRoleUser MessageRole = "user"
	// MessageRoleAgent is the role of a message originated from the agent/server.
	MessageRoleAgent MessageRole = "agent"
)

// PartType indicates the type of content within a message part.
// See A2A Spec section on Message Parts.
type PartType string

// PartType constants define the supported types for message parts.
const (
	// PartTypeText is for simple text content.
	PartTypeText PartType = "text"
	// PartTypeFile is for file references (path or URL).
	PartTypeFile PartType = "file"
	// PartTypeData is for raw binary data.
	PartTypeData PartType = "data"
)

// FileContent represents file data, either directly embedded or via URI.
// Corresponds to the 'file' structure in A2A Message Parts.
type FileContent struct {
	// Name is the optional filename.
	Name *string `json:"name,omitempty"`
	// MimeType is the optional MIME type.
	MimeType *string `json:"mimeType,omitempty"`
	// Bytes is the optional base64-encoded content.
	Bytes *string `json:"bytes,omitempty"`
	// URI is the optional URI pointing to the content.
	URI *string `json:"uri,omitempty"`
}

// TextPart represents a text segment within a message.
// Corresponds to the 'text' part type in A2A Message Parts.
type TextPart struct {
	// Type is the type of the part.
	Type PartType `json:"type"`
	// Text is the text content.
	Text string `json:"text"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// FilePart represents a file included in a message.
// Corresponds to the 'file' part type in A2A Message Parts.
type FilePart struct {
	// Type is the type of the part.
	Type PartType `json:"type"`
	// File is the file content.
	File FileContent `json:"file"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// DataPart represents arbitrary structured data (JSON) within a message.
// Corresponds to the 'data' part type in A2A Message Parts.
type DataPart struct {
	// Type is the type of the part.
	Type PartType `json:"type"`
	// Data is the actual data payload.
	Data interface{} `json:"data"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// AuthenticationInfo represents authentication details for external services.
type AuthenticationInfo struct {
	// Schemes is a list of authentication schemes supported.
	Schemes []string `json:"schemes"`
	// Credentials are the actual authentication credentials.
	Credentials string `json:"credentials,omitempty"`
	// OAuth2 contains OAuth2 specific authentication configuration.
	OAuth2 *OAuth2AuthInfo `json:"oauth2,omitempty"`
	// JWT contains JWT specific authentication configuration.
	JWT *JWTAuthInfo `json:"jwt,omitempty"`
	// APIKey contains API key specific authentication configuration.
	APIKey *APIKeyAuthInfo `json:"apiKey,omitempty"`
}

// OAuth2AuthInfo contains OAuth2-specific authentication details.
type OAuth2AuthInfo struct {
	// ClientID is the OAuth2 client ID.
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret is the OAuth2 client secret.
	ClientSecret string `json:"clientSecret,omitempty"`
	// TokenURL is the OAuth2 token endpoint.
	TokenURL string `json:"tokenUrl,omitempty"`
	// AuthURL is the OAuth2 authorization endpoint (for authorization code flow).
	AuthURL string `json:"authUrl,omitempty"`
	// Scopes is a list of OAuth2 scopes to request.
	Scopes []string `json:"scopes,omitempty"`
	// RefreshToken is the OAuth2 refresh token.
	RefreshToken string `json:"refreshToken,omitempty"`
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"accessToken,omitempty"`
}

// JWTAuthInfo contains JWT-specific authentication details.
type JWTAuthInfo struct {
	// Token is a pre-generated JWT token.
	Token string `json:"token,omitempty"`
	// KeyID is the ID of the key used to sign the JWT.
	KeyID string `json:"keyId,omitempty"`
	// JWKSURL is the URL of the JWKS endpoint for key discovery.
	JWKSURL string `json:"jwksUrl,omitempty"`
	// Audience is the expected audience claim.
	Audience string `json:"audience,omitempty"`
	// Issuer is the expected issuer claim.
	Issuer string `json:"issuer,omitempty"`
}

// APIKeyAuthInfo contains API key-specific authentication details.
type APIKeyAuthInfo struct {
	// Key is the API key value.
	Key string `json:"key,omitempty"`
	// HeaderName is the name of the header to include the API key in.
	HeaderName string `json:"headerName,omitempty"`
	// ParamName is the name of the query parameter to include the API key in.
	ParamName string `json:"paramName,omitempty"`
	// Location specifies where to place the API key (header, query, or cookie).
	Location string `json:"location,omitempty"`
}

// PushNotificationConfig represents the configuration for task push notifications.
type PushNotificationConfig struct {
	// URL is the endpoint where notifications should be sent.
	URL string `json:"url"`
	// Token is an optional authentication token.
	Token string `json:"token,omitempty"`
	// Authentication contains optional authentication details.
	Authentication *AuthenticationInfo `json:"authentication,omitempty"`
	// Metadata is optional additional configuration data.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// TaskPushNotificationConfig associates a task ID with push notification settings.
type TaskPushNotificationConfig struct {
	// ID is the unique task identifier.
	ID string `json:"id"`
	// PushNotificationConfig contains the notification settings.
	PushNotificationConfig PushNotificationConfig `json:"pushNotificationConfig"`
}

// Part is an interface representing a segment of a message (text, file, or data).
// It uses an unexported method to ensure only defined part types implement it.
// See A2A Spec section on Message Parts.
// Exported interface.
type Part interface {
	partMarker() // Internal marker method.
}

// partMarker implementations for concrete types (unexported methods).
func (TextPart) partMarker() {}
func (FilePart) partMarker() {}
func (DataPart) partMarker() {}

// Message represents a single exchange between a user and an agent.
// See A2A Spec section on Messages.
type Message struct {
	// Role is the sender of the message.
	Role MessageRole `json:"role"`
	// Parts is the content parts (must implement Part).
	Parts []Part `json:"parts"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// UnmarshalJSON implements custom unmarshalling logic for Message
// to handle the polymorphic Part interface slice.
func (m *Message) UnmarshalJSON(data []byte) error {
	type Alias Message // Alias to avoid recursion.
	temp := &struct {
		Parts []json.RawMessage `json:"parts"` // Unmarshal parts into RawMessage first.
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal message base: %w", err)
	}
	// Now, unmarshal each part based on its type.
	m.Parts = make([]Part, 0, len(temp.Parts))
	for i, rawPart := range temp.Parts {
		part, err := unmarshalPart(rawPart)
		if err != nil {
			return fmt.Errorf("failed to unmarshal part %d: %w", i, err)
		}
		m.Parts = append(m.Parts, part)
	}
	return nil
}

// Artifact represents an output generated by a task, potentially chunked for streaming.
// See A2A Spec section on Artifacts.
type Artifact struct {
	// Name is the name of the artifact.
	Name *string `json:"name,omitempty"`
	// Description is the description of the artifact.
	Description *string `json:"description,omitempty"`
	// Parts is the content parts of the artifact.
	Parts []Part `json:"parts"`
	// Index is the index for ordering streamed artifacts.
	Index int `json:"index"`
	// Append is a hint for the client to append data (streaming).
	Append *bool `json:"append,omitempty"`
	// LastChunk is a flag indicating if this is the final chunk of an artifact stream.
	LastChunk *bool `json:"lastChunk,omitempty"`
	// Metadata is optional metadata for the artifact.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// UnmarshalJSON implements custom unmarshalling logic for Artifact
// to handle the polymorphic Part interface slice.
func (a *Artifact) UnmarshalJSON(data []byte) error {
	type Alias Artifact // Alias to avoid recursion.
	temp := &struct {
		Parts []json.RawMessage `json:"parts"` // Unmarshal parts into RawMessage first.
		*Alias
	}{
		Alias: (*Alias)(a),
	}
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal artifact base: %w", err)
	}
	// Now, unmarshal each part based on its type.
	a.Parts = make([]Part, 0, len(temp.Parts))
	for i, rawPart := range temp.Parts {
		part, err := unmarshalPart(rawPart)
		if err != nil {
			return fmt.Errorf("failed to unmarshal artifact part %d: %w", i, err)
		}
		a.Parts = append(a.Parts, part)
	}
	return nil
}

// unmarshalPart determines the concrete type of a Part from raw JSON
// based on the "type" field and unmarshals into that concrete type.
// Internal helper function.
func unmarshalPart(rawPart json.RawMessage) (Part, error) {
	// Peek at the type field to determine the concrete type.
	var typeDetect struct {
		Type PartType `json:"type"`
	}
	if err := json.Unmarshal(rawPart, &typeDetect); err != nil {
		return nil, fmt.Errorf("cannot detect part type: %w. Data: %s", err, string(rawPart))
	}
	// Unmarshal into the correct concrete type.
	switch typeDetect.Type {
	case PartTypeText:
		var p TextPart
		if err := json.Unmarshal(rawPart, &p); err != nil {
			return nil, fmt.Errorf("failed to unmarshal TextPart: %w", err)
		}
		return p, nil
	case PartTypeFile:
		var p FilePart
		if err := json.Unmarshal(rawPart, &p); err != nil {
			return nil, fmt.Errorf("failed to unmarshal FilePart: %w", err)
		}
		return p, nil
	case PartTypeData:
		var p DataPart
		if err := json.Unmarshal(rawPart, &p); err != nil {
			return nil, fmt.Errorf("failed to unmarshal DataPart: %w", err)
		}
		return p, nil
	default:
		// If we need to handle unknown part types gracefully (e.g., store raw JSON),
		// we would add that logic here. For now, treat as an error.
		return nil, fmt.Errorf("unsupported part type: %s", typeDetect.Type)
	}
}

// TaskStatus represents the current status of a task.
// See A2A Spec section on Task Lifecycle.
type TaskStatus struct {
	// State is the current lifecycle state.
	State TaskState `json:"state"`
	// Message is the optional message associated with the status (e.g., final response).
	Message *Message `json:"message,omitempty"`
	// Timestamp is the ISO 8601 timestamp of the status change.
	Timestamp string `json:"timestamp"`
}

// Task represents a unit of work being processed by the agent.
// See A2A Spec section on Tasks.
type Task struct {
	// ID is the unique task identifier.
	ID string `json:"id"`
	// SessionID is the optional session identifier for grouping tasks.
	SessionID *string `json:"sessionId,omitempty"`
	// Status is the current task status.
	Status TaskStatus `json:"status"`
	// Artifacts is the accumulated artifacts generated by the task.
	Artifacts []Artifact `json:"artifacts,omitempty"`
	// History is the history of messages exchanged for this task (if tracked).
	History []Message `json:"history,omitempty"`
	// Metadata is the optional metadata associated with the task.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// TaskEvent is an interface for events published during task execution (streaming).
// It uses an unexported method to ensure only defined event types implement it.
// See A2A Spec section on Streaming and Events.
// Exported interface.
type TaskEvent interface {
	eventMarker() // Internal marker method.
	// IsFinal returns true if this is the final event for the task.
	IsFinal() bool
}

// TaskStatusUpdateEvent indicates a change in the task's lifecycle state.
// Corresponds to the 'task_status_update' event in A2A Spec.
type TaskStatusUpdateEvent struct {
	// ID is the ID of the task.
	ID string `json:"id"`
	// Status is the new status.
	Status TaskStatus `json:"status"`
	// Final is a flag indicating if this is the terminal status event.
	Final bool `json:"final"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// eventMarker implementation (unexported method).
func (TaskStatusUpdateEvent) eventMarker() {}

// IsFinal implements TaskEvent.
func (e TaskStatusUpdateEvent) IsFinal() bool {
	return e.Final
}

// TaskArtifactUpdateEvent indicates a new or updated artifact chunk.
// Corresponds to the 'task_artifact_update' event in A2A Spec.
type TaskArtifactUpdateEvent struct {
	// ID is the ID of the task.
	ID string `json:"id"`
	// Artifact is the artifact data.
	Artifact Artifact `json:"artifact"`
	// Final is a flag indicating if this is the final event for the task (usually linked to Artifact.LastChunk).
	Final bool `json:"final"`
	// Metadata is optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// eventMarker implementation (unexported method).
func (TaskArtifactUpdateEvent) eventMarker() {}

// IsFinal implements TaskEvent.
func (e TaskArtifactUpdateEvent) IsFinal() bool {
	return e.Final
}

// SendTaskParams defines the parameters for the tasks_send and tasks_sendSubscribe RPC methods.
// See A2A Spec section on RPC Methods.
type SendTaskParams struct {
	// ID is the ID of the task.
	ID string `json:"id"`
	// SessionID is the optional session ID.
	SessionID *string `json:"sessionId,omitempty"`
	// Message is the user's message initiating the task.
	Message Message `json:"message"`
	// HistoryLength is the requested history length in response.
	HistoryLength *int `json:"historyLength,omitempty"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// TaskQueryParams defines the parameters for the tasks_get RPC method.
// See A2A Spec section on RPC Methods.
type TaskQueryParams struct {
	// ID is the ID of the task.
	ID string `json:"id"`
	// HistoryLength is the requested message history length.
	HistoryLength *int `json:"historyLength,omitempty"`
}

// TaskIDParams defines parameters for methods needing only a task ID (e.g., tasks_cancel).
// See A2A Spec section on RPC Methods.
type TaskIDParams struct {
	// ID is the ID of the task.
	ID string `json:"id"`
}

// --- Factory Functions ---

// NewTask creates a new Task with initial state (Submitted).
func NewTask(id string, sessionID *string) *Task {
	return &Task{
		ID:        id,
		SessionID: sessionID,
		Status: TaskStatus{
			State:     TaskStateSubmitted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Metadata: make(map[string]interface{}),
	}
}

// NewMessage creates a new Message with the specified role and parts.
func NewMessage(role MessageRole, parts []Part) Message {
	return Message{
		Role:  role,
		Parts: parts,
	}
}

// NewTextPart creates a new TextPart containing the given text.
func NewTextPart(text string) TextPart {
	return TextPart{
		Type: PartTypeText,
		Text: text,
	}
}
