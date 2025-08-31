// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package protocol defines the core types and interfaces based on the A2A specification.
package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "trpc.group/trpc-go/trpc-a2a-go/protocol/a2apb"
)

// TaskState represents the lifecycle state of a task.
// See A2A Spec section on Task Lifecycle.
type TaskState = v1.TaskState

// TaskState constants define the possible states of a task.
const (
	// TaskStateSubmitted is the state when the task is received but not yet processed.
	TaskStateSubmitted TaskState = v1.TaskState_TASK_STATE_SUBMITTED
	// TaskStateWorking is the state when the task is actively being processed.
	TaskStateWorking TaskState = v1.TaskState_TASK_STATE_WORKING
	// TaskStateInputRequired is the state when the task requires additional input (semantics evolving in spec).
	TaskStateInputRequired TaskState = v1.TaskState_TASK_STATE_INPUT_REQUIRED
	// TaskStateCompleted is the state when the task finished successfully.
	TaskStateCompleted TaskState = v1.TaskState_TASK_STATE_COMPLETED
	// TaskStateCanceled is the state when the task was canceled before completion.
	TaskStateCanceled TaskState = v1.TaskState_TASK_STATE_CANCELLED
	// TaskStateFailed is the state when the task failed during processing.
	TaskStateFailed TaskState = v1.TaskState_TASK_STATE_FAILED
	// TaskStateRejected is the state when the task was rejected by the agent.
	TaskStateRejected TaskState = v1.TaskState_TASK_STATE_REJECTED
	// TaskStateAuthRequired is the state when the task requires authentication before processing.
	TaskStateAuthRequired TaskState = v1.TaskState_TASK_STATE_AUTH_REQUIRED
	// TaskStateUnknown is the state when the task is in an unknown or indeterminate state.
	TaskStateUnknown TaskState = v1.TaskState_TASK_STATE_UNSPECIFIED
)

// Event is an interface that represents the kind of the struct.
type Event interface {
	GetKind() string
}

// GetKind returns the kind of the result.
func (m Message) GetKind() string { return KindMessage }

// GetKind returns the kind of the task.
func (t Task) GetKind() string { return KindTask }

// GetKind returns the kind of the task status update event.
func (r *TaskStatusUpdateEvent) GetKind() string { return KindTaskStatusUpdate }

// GetKind returns the kind of the task artifact update event.
func (r *TaskArtifactUpdateEvent) GetKind() string { return KindTaskArtifactUpdate }

// GenerateMessageID generates a new unique message ID.
func GenerateMessageID() string {
	id := uuid.New()
	return "msg-" + id.String()
}

// GenerateContextID generates a new unique context ID for a task.
func GenerateContextID() string {
	id := uuid.New()
	return "ctx-" + id.String()
}

// GenerateTaskID generates a new unique task ID.
func GenerateTaskID() string {
	id := uuid.New()
	return "task-" + id.String()
}

// GenerateArtifactID generates a new unique artifact ID.
func GenerateArtifactID() string {
	id := uuid.New()
	return "artifact-" + id.String()
}

// GenerateRPCID generates a new unique RPC ID.
func GenerateRPCID() string {
	id := uuid.New()
	return id.String()
}

// UnaryMessageResult is an interface representing a result of SendMessage.
// It only supports Message or Task.
type UnaryMessageResult interface {
	unaryMessageResultMarker()
	GetKind() string
}

func (Message) unaryMessageResultMarker() {}
func (Task) unaryMessageResultMarker()    {}

// Part is an interface representing a segment of a message (text, file, or data).
// It uses an unexported method to ensure only defined part types implement it.
// See A2A Spec section on Message Parts.
// Exported interface.
type Part = v1.Part

// StreamingMessageResult is an interface representing a result of SendMessageStream.
type StreamingMessageResult interface {
	streamingMessageResultMarker()
	GetKind() string
}

func (Message) streamingMessageResultMarker()                 {}
func (Task) streamingMessageResultMarker()                    {}
func (TaskStatusUpdateEvent) streamingMessageResultMarker()   {}
func (TaskArtifactUpdateEvent) streamingMessageResultMarker() {}

// Kind constants define the possible kinds of the struct.
const (
	// KindMessage is the kind of the message.
	KindMessage = "message"
	// KindTask is the kind of the task.
	KindTask = "task"
	// KindTaskStatusUpdate is the kind of the task status update event.
	KindTaskStatusUpdate = "status-update"
	// KindTaskArtifactUpdate is the kind of the task artifact update event.
	KindTaskArtifactUpdate = "artifact-update"
	// KindData is the kind of the data.
	KindData = "data"
	// KindFile is the kind of the file.
	KindFile = "file"
	// KindText is the kind of the text.
	KindText = "text"
)

// MessageRole indicates the originator of a message (user or agent).
// See A2A Spec section on Messages.
type MessageRole = v1.Role

// MessageRole constants define the possible roles for a message sender.
const (
	// MessageRoleUser is the role of a message originated from the user/client.
	MessageRoleUser MessageRole = v1.Role_ROLE_USER
	// MessageRoleAgent is the role of a message originated from the agent/server.
	MessageRoleAgent MessageRole = v1.Role_ROLE_AGENT
	// MessageRoleUnknown is the role of a message with an unknown originator.
	MessageRoleUnknown MessageRole = v1.Role_ROLE_UNSPECIFIED
)

// FileUnion represents the union type for file content.
// It contains either FileWithBytes or FileWithURI.
type FileUnion interface {
	fileUnionMarker()
}

func (f *FileWithBytes) fileUnionMarker() {}
func (f *FileWithURI) fileUnionMarker()   {}

// TextPart represents a text segment within a message.
// Corresponds to the 'text' part type in A2A Message Parts.
type TextPart = v1.Part_Text

// FilePart represents a file included in a message.
// Corresponds to the 'file' part type in A2A Message Parts.
type FilePart = v1.Part_File

// DataPart represents arbitrary structured data (JSON) within a message.
// Corresponds to the 'data' part type in A2A Message Parts.
type DataPart = v1.DataPart

// FileWithBytes represents file data with embedded content.
// This is one variant of the File union type in A2A 0.2.2.
type FileWithBytes struct {
	// Name is the optional filename.
	Name *string `json:"name,omitempty"`
	// MimeType is the optional MIME type.
	MimeType *string `json:"mimeType,omitempty"`
	// Bytes is the required base64-encoded content.
	Bytes string `json:"bytes"`
}

// FileWithURI represents file data with URI reference.
// This is one variant of the File union type in A2A 0.2.2.
type FileWithURI struct {
	// Name is the optional filename.
	Name *string `json:"name,omitempty"`
	// MimeType is the optional MIME type.
	MimeType *string `json:"mimeType,omitempty"`
	// URI is the required URI pointing to the content.
	URI string `json:"uri"`
}

// PushNotificationAuthenticationInfo represents authentication details for external services.
// Renamed from AuthenticationInfo for A2A 0.2.2 specification compliance.
type PushNotificationAuthenticationInfo struct {
	// Credentials are the actual authentication credentials.
	Credentials *string `json:"credentials,omitempty"`
	// Schemes is a list of authentication schemes supported.
	Schemes []string `json:"schemes"`
}

// AuthenticationInfo is deprecated, use PushNotificationAuthenticationInfo instead.
type AuthenticationInfo = PushNotificationAuthenticationInfo

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
type PushNotificationConfig = v1.PushNotificationConfig

// TaskPushNotificationConfig associates a task ID with push notification settings.
type TaskPushNotificationConfig struct {
	// TaskID is extracted from TaskPushNotificationConfig's name
	TaskID string `json:"taskId,omitempty"`
	// ConfigID is extracted from TaskPushNotificationConfig's name
	ConfigID string `json:"configId,omitempty"`
	// Raw specification of the push notification config.
	*v1.TaskPushNotificationConfig
}

// Parse parses the TaskPushNotificationConfig's name and extract out TaskID and ConfigID.
func (t *TaskPushNotificationConfig) Parse() error {
	rg := regexp.MustCompile(`^tasks/([^/]+)/pushNotificationConfigs/([^/]+)$`)
	parts := rg.FindStringSubmatch(t.Name)
	if len(parts) != 3 {
		return fmt.Errorf("invalid TaskPushNotificationConfig name format: %s", t.Name)
	}
	t.TaskID = parts[1]
	t.ConfigID = parts[2]
	return nil
}

// Message represents a single exchange between a user and an agent.
// See A2A Spec section on Messages.
type Message struct {
	*v1.Message
}

// UnmarshalJSON implements custom unmarshalling logic for Message
// to handle the polymorphic Part interface slice.
func (m *Message) UnmarshalJSON(data []byte) error {
	var err error
	data, err = sjson.DeleteBytes(data, "kind")
	if err != nil {
		return err
	}
	var mm v1.Message
	if err := protojson.Unmarshal(data, &mm); err != nil {
		return err
	}
	m.Message = &mm
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	pj, err := protojson.Marshal(m.Message)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(pj, "kind", []byte(KindMessage))
}

// Artifact represents an output generated by a task.
// See A2A Spec 0.2.2 section on Artifacts.
type Artifact struct {
	*v1.Artifact
}

// UnmarshalJSON implements custom unmarshalling logic for Artifact
// to handle the polymorphic Part interface slice.
func (a *Artifact) UnmarshalJSON(data []byte) error {
	var err error
	data, err = sjson.DeleteBytes(data, "kind")
	if err != nil {
		return err
	}
	var af v1.Artifact
	if err := protojson.Unmarshal(data, &af); err != nil {
		return err
	}
	a.Artifact = &af
	return nil
}

func (a Artifact) MarshalJSON() ([]byte, error) {
	pj, err := protojson.Marshal(a.Artifact)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(pj, "kind", []byte(KindTaskArtifactUpdate))
}

// unmarshalPart determines the concrete type of a Part from raw JSON
// based on the "kind" field and unmarshals into that concrete type.
// Internal helper function.
func unmarshalPart(rawPart json.RawMessage) (Part, error) {
	// First, determine the type by unmarshalling just the "kind" field.
	part := v1.Part{}
	if err := json.Unmarshal(rawPart, &part); err != nil {
		return Part{}, fmt.Errorf("failed to unmarshal part kind: %w", err)
	}
	switch {
	case part.GetData() != nil:
		return Part{
			Part: &v1.Part_Data{
				Data: part.GetData(),
			},
		}, nil
	case part.GetFile() != nil:
		return Part{
			Part: &v1.Part_File{
				File: part.GetFile(),
			},
		}, nil
	case part.GetText() != "":
		return Part{
			Part: &v1.Part_Text{
				Text: part.GetText(),
			},
		}, nil
	}
	return Part{}, fmt.Errorf("unknown part kind")
}

// TaskStatus represents the current status of a task.
// See A2A Spec section on Task Lifecycle.
type TaskStatus = v1.TaskStatus

// Task represents a unit of work being processed by the agent.
// See A2A Spec section on Tasks.
type Task struct {
	*v1.Task
}

func (t *Task) UnmarshalJSON(data []byte) error {
	var err error
	data, err = sjson.DeleteBytes(data, "kind")
	if err != nil {
		return err
	}
	var vt v1.Task
	if err := protojson.Unmarshal(data, &vt); err != nil {
		return err
	}
	t.Task = &vt
	return nil
}

func (t Task) MarshalJSON() ([]byte, error) {
	pj, err := protojson.Marshal(t)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(pj, "kind", []byte(KindTask))
}

// TaskStatusUpdateEvent indicates a change in the task's lifecycle state.
// Corresponds to the 'task_status_update' event in A2A Spec 0.2.2.
type TaskStatusUpdateEvent struct {
	*v1.TaskStatusUpdateEvent
}

// IsFinal returns true if this is a final event.
func (r TaskStatusUpdateEvent) IsFinal() bool {
	return r.Final
}

func (r TaskStatusUpdateEvent) MarshalJSON() ([]byte, error) {
	pj, err := protojson.Marshal(r)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(pj, "kind", []byte(KindTaskStatusUpdate))
}

func (r *TaskStatusUpdateEvent) UnmarshalJSON(data []byte) error {
	var err error
	data, err = sjson.DeleteBytes(data, "kind")
	if err != nil {
		return err
	}
	var vt v1.TaskStatusUpdateEvent
	if err := protojson.Unmarshal(data, &vt); err != nil {
		return err
	}
	r.TaskStatusUpdateEvent = &vt
	return nil
}

// TaskArtifactUpdateEvent indicates a new or updated artifact chunk.
// Corresponds to the 'task_artifact_update' event in A2A Spec 0.2.2.
type TaskArtifactUpdateEvent struct {
	*v1.TaskArtifactUpdateEvent
}

// IsFinal returns true if this is the final artifact event.
func (r TaskArtifactUpdateEvent) IsFinal() bool {
	return r.LastChunk
}

func (r TaskArtifactUpdateEvent) MarshalJSON() ([]byte, error) {
	pj, err := protojson.Marshal(r)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(pj, "kind", []byte(KindTaskArtifactUpdate))
}

func (r *TaskArtifactUpdateEvent) UnmarshalJSON(data []byte) error {
	var err error
	data, err = sjson.DeleteBytes(data, "kind")
	if err != nil {
		return err
	}
	var vt v1.TaskArtifactUpdateEvent
	if err := protojson.Unmarshal(data, &vt); err != nil {
		return err
	}
	r.TaskArtifactUpdateEvent = &vt
	return nil
}

// NewTask creates a new Task with initial state (Submitted).
func NewTask(id string, contextID string) *Task {
	metadata, _ := structpb.NewStruct(map[string]any{})
	return &Task{
		Task: &v1.Task{
			Id:        id,
			ContextId: contextID,
			Status: &v1.TaskStatus{
				State:     TaskStateSubmitted,
				Timestamp: timestamppb.New(time.Now()),
			},
			Metadata: metadata,
		},
	}
}

// NewMessage creates a new Message with the specified role and parts.
func NewMessage(role MessageRole, parts []*Part) Message {
	messageID := GenerateMessageID()
	return Message{
		Message: &v1.Message{
			Role:      role,
			Content:   parts,
			MessageId: messageID,
		},
	}
}

// NewMessageWithContext creates a new Message with context information.
func NewMessageWithContext(role MessageRole, parts []*Part, taskID, contextID *string) Message {
	messageID := GenerateMessageID()
	return Message{
		Message: &v1.Message{
			Role:      role,
			Content:   parts,
			MessageId: messageID,
			TaskId:    *taskID,
			ContextId: *contextID,
		},
	}
}

// NewTextPart creates a new TextPart containing the given text.
func NewTextPart(text string) *TextPart {
	return &TextPart{
		Text: text,
	}
}

// NewFilePartWithBytes creates a new FilePart with embedded bytes content.
func NewFilePartWithBytes(name, mimeType string, bytes string) *FilePart {
	return &FilePart{
		File: &v1.FilePart{
			File: &v1.FilePart_FileWithBytes{FileWithBytes: []byte(bytes)},
		},
	}
}

// NewFilePartWithURI creates a new FilePart with URI reference.
func NewFilePartWithURI(name, mimeType string, uri string) *FilePart {
	return &FilePart{
		File: &v1.FilePart{File: &v1.FilePart_FileWithUri{
			FileWithUri: uri,
		}},
	}
}

// NewDataPart creates a new DataPart with the given data.
func NewDataPart(data map[string]any) (DataPart, error) {
	dataStruct, err := structpb.NewStruct(data)
	if err != nil {
		return DataPart{}, fmt.Errorf("failed to create structpb.Struct from data: %w", err)
	}
	return DataPart{
		Data: dataStruct,
	}, nil
}

// NewArtifactWithID creates a new Artifact with a generated ID.
func NewArtifactWithID(name, description *string, parts []*Part) *Artifact {
	artifactID := GenerateArtifactID()
	// ignore error here for simplicity, since metadata is empty here
	md, _ := structpb.NewStruct(map[string]any{})
	return &Artifact{
		Artifact: &v1.Artifact{
			ArtifactId:  artifactID,
			Name:        *name,
			Description: *description,
			Parts:       parts,
			Metadata:    md,
		},
	}
}

// NewTaskStatusUpdateEvent creates a new TaskStatusUpdateEvent.
func NewTaskStatusUpdateEvent(taskID, contextID string, status *TaskStatus, final bool) TaskStatusUpdateEvent {
	return TaskStatusUpdateEvent{
		TaskStatusUpdateEvent: &v1.TaskStatusUpdateEvent{
			TaskId:    taskID,
			ContextId: contextID,
			Status:    status,
			Final:     final,
		},
	}
}

// NewTaskArtifactUpdateEvent creates a new TaskArtifactUpdateEvent.
func NewTaskArtifactUpdateEvent(taskID, contextID string, artifact Artifact, lastChunk bool) TaskArtifactUpdateEvent {
	return TaskArtifactUpdateEvent{
		TaskArtifactUpdateEvent: &v1.TaskArtifactUpdateEvent{
			TaskId:    taskID,
			ContextId: contextID,
			Artifact:  artifact.Artifact,
			LastChunk: lastChunk,
		},
	}
}

// TaskQueryParams defines the parameters for the tasks_get RPC method.
// See A2A Spec section on RPC Methods.
type TaskQueryParams struct {
	// RPCID is the ID of json-rpc.
	RPCID string `json:"-"`
	// ID is the ID of the task.
	ID string `json:"id"`
	// HistoryLength is the requested message history length.
	HistoryLength *int `json:"historyLength,omitempty"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// TaskIDParams defines parameters for methods needing only a task ID (e.g., tasks_cancel).
// See A2A Spec section on RPC Methods.
type TaskIDParams struct {
	// RPCID is the ID of json-rpc.
	RPCID string `json:"-"`
	// ID is task ID.
	ID string `json:"id"`
	// Metadata is the optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SendMessageParams defines the parameters for the message/send and message/stream RPC methods.
// See A2A Spec 0.2.2 section on RPC Methods.
type SendMessageParams struct {
	// RPCID is the ID of json-rpc.
	RPCID string `json:"-"`
	// Configuration contains optional sending configuration.
	Configuration *SendMessageConfiguration `json:"configuration,omitempty"`
	// Message is the message to send.
	Message Message `json:"message"`
	// Metadata is optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SendMessageConfiguration defines optional configuration for message sending.
type SendMessageConfiguration = v1.SendMessageConfiguration

// MessageResult represents the union type response for Message/Task.
type MessageResult struct {
	Result UnaryMessageResult
}

// UnmarshalJSON implements custom unmarshalling logic for MessageResult
func (r *MessageResult) UnmarshalJSON(data []byte) error {
	// First, detect the kind of the result
	var kindOnly struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &kindOnly); err != nil {
		return fmt.Errorf("failed to unmarshal result kind: %w", err)
	}

	// Now unmarshal to the correct concrete type based on kind
	switch kindOnly.Kind {
	case KindMessage:
		r.Result = &Message{}
		if err := json.Unmarshal(data, r.Result); err != nil {
			return fmt.Errorf("failed to unmarshal message: %w", err)
		}
		return nil
	case KindTask:
		r.Result = &Task{}
		if err := json.Unmarshal(data, r.Result); err != nil {
			return fmt.Errorf("failed to unmarshal task: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported result kind: %s", kindOnly.Kind)
	}
}

// MarshalJSON implements custom marshalling logic for MessageResult
func (r *MessageResult) MarshalJSON() ([]byte, error) {
	switch r.Result.GetKind() {
	case KindMessage:
		return json.Marshal(r.Result)
	case KindTask:
		return json.Marshal(r.Result)
	default:
		return nil, fmt.Errorf("unsupported result kind: %s", r.Result.GetKind())
	}
}

// SendStreamingMessageParams defines the parameters for the message/stream RPC method.
type SendStreamingMessageParams struct {
	// ID is the ID of the message.
	ID string `json:"-"`
	// Configuration contains optional sending configuration.
	Configuration *SendMessageConfiguration `json:"configuration,omitempty"`
	// Message is the message to send.
	Message Message `json:"message"`
	// Metadata is optional metadata.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// StreamingMessageEvent represents the result of a streaming message operation.
// StreamingMessageEvent is the union type of Message/Task/TaskStatusUpdate/TaskArtifactUpdate.
type StreamingMessageEvent struct {
	// Result is the final result of the streaming operation.
	Result StreamingMessageResult
}

// UnmarshalJSON implements custom unmarshalling logic for StreamingMessageEvent
func (r *StreamingMessageEvent) UnmarshalJSON(data []byte) error {
	// First, try to detect if this is wrapped in a Result field
	type StreamingMessageEventRaw struct {
		Result json.RawMessage `json:"Result"`
	}

	var raw StreamingMessageEventRaw
	var actualData []byte

	// Try to unmarshal as wrapped structure first
	if err := json.Unmarshal(data, &raw); err == nil && len(raw.Result) > 0 {
		// It's wrapped, use the Result field
		actualData = raw.Result
	} else {
		// It's not wrapped, use the data directly
		actualData = data
	}

	// Parse the actual data to get the kind
	var kindOnly struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(actualData, &kindOnly); err != nil {
		return fmt.Errorf("failed to unmarshal result kind: %w", err)
	}

	// Now unmarshal to the correct concrete type based on kind
	switch kindOnly.Kind {
	case KindMessage:
		r.Result = &Message{}
		if err := json.Unmarshal(actualData, r.Result); err != nil {
			return fmt.Errorf("failed to unmarshal message: %w", err)
		}
		return nil
	case KindTask:
		r.Result = &Task{}
		if err := json.Unmarshal(actualData, r.Result); err != nil {
			return fmt.Errorf("failed to unmarshal task: %w", err)
		}
		return nil
	case KindTaskStatusUpdate:
		r.Result = &TaskStatusUpdateEvent{}
		if err := json.Unmarshal(actualData, r.Result); err != nil {
			return fmt.Errorf("failed to unmarshal task status update event: %w", err)
		}
		return nil
	case KindTaskArtifactUpdate:
		r.Result = &TaskArtifactUpdateEvent{}
		if err := json.Unmarshal(actualData, r.Result); err != nil {
			return fmt.Errorf("failed to unmarshal task artifact update event: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported result kind: %s", kindOnly.Kind)
	}
}

// MarshalJSON implements custom marshalling logic for StreamingMessageResult
func (r *StreamingMessageEvent) MarshalJSON() ([]byte, error) {
	switch r.Result.GetKind() {
	case KindMessage:
		return json.Marshal(r.Result)
	case KindTask:
		return json.Marshal(r.Result)
	case KindTaskStatusUpdate:
		return json.Marshal(r.Result)
	case KindTaskArtifactUpdate:
		return json.Marshal(r.Result)
	default:
		return nil, fmt.Errorf("unsupported result kind: %s", r.Result.GetKind())
	}
}
