// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"encoding/base64"
	"fmt"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// This file implements the bidirectional translation between the frozen v0.2.x
// wire types (this package) and the v1.0 core types (protocol package).
// Every ToV1Xxx has a FromV1Xxx inverse; the pairs are designed to round-trip
// without information loss for all data representable in both protocols.
//
// Known asymmetries (v1 -> v0 lossy by protocol design):
//   - v1 carries filename/mediaType on every part; v0 only on file parts.
//     For text/data parts these fields are dropped when converting to v0.
//   - v1 stream events have no "final" flag; it is derived from the task state
//     when converting to v0.

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

var v0ToV1TaskState = map[TaskState]protocol.TaskState{
	TaskStateSubmitted:     protocol.TaskStateSubmitted,
	TaskStateWorking:       protocol.TaskStateWorking,
	TaskStateInputRequired: protocol.TaskStateInputRequired,
	TaskStateCompleted:     protocol.TaskStateCompleted,
	TaskStateCanceled:      protocol.TaskStateCanceled,
	TaskStateFailed:        protocol.TaskStateFailed,
	TaskStateRejected:      protocol.TaskStateRejected,
	TaskStateAuthRequired:  protocol.TaskStateAuthRequired,
	TaskStateUnknown:       protocol.TaskStateUnspecified,
}

var v1ToV0TaskState map[protocol.TaskState]TaskState

func init() {
	v1ToV0TaskState = make(map[protocol.TaskState]TaskState, len(v0ToV1TaskState))
	for v0State, v1State := range v0ToV1TaskState {
		v1ToV0TaskState[v1State] = v0State
	}
	// Both "unknown" and the v1 zero value map to each other already; make the
	// legacy "cancelled" spelling (used by some old peers) parse as canceled.
	v1ToV0TaskState[protocol.TaskStateUnspecified] = TaskStateUnknown
}

// ToV1TaskState converts a legacy task state to its v1 equivalent.
func ToV1TaskState(s TaskState) protocol.TaskState {
	if s == "cancelled" { // alternative spelling seen in old peers
		return protocol.TaskStateCanceled
	}
	if mapped, ok := v0ToV1TaskState[s]; ok {
		return mapped
	}
	return protocol.TaskStateUnspecified
}

// FromV1TaskState converts a v1 task state to its legacy equivalent.
func FromV1TaskState(s protocol.TaskState) TaskState {
	if mapped, ok := v1ToV0TaskState[s]; ok {
		return mapped
	}
	return TaskStateUnknown
}

// ToV1Role converts a legacy message role to its v1 equivalent.
func ToV1Role(r MessageRole) protocol.MessageRole {
	switch r {
	case MessageRoleUser:
		return protocol.MessageRoleUser
	case MessageRoleAgent:
		return protocol.MessageRoleAgent
	default:
		return protocol.MessageRoleUnspecified
	}
}

// FromV1Role converts a v1 message role to its legacy equivalent.
func FromV1Role(r protocol.MessageRole) MessageRole {
	switch r {
	case protocol.MessageRoleUser:
		return MessageRoleUser
	case protocol.MessageRoleAgent:
		return MessageRoleAgent
	default:
		// v0 has no unspecified role; default to user, which old peers expect
		// for inbound traffic.
		return MessageRoleUser
	}
}

// ---------------------------------------------------------------------------
// Parts
// ---------------------------------------------------------------------------

// ToV1Part converts a legacy part (kind-discriminated) to a v1 unified part.
func ToV1Part(p Part) *protocol.Part {
	switch v := p.(type) {
	case TextPart:
		return &protocol.Part{Content: protocol.Text(v.Text), Metadata: v.Metadata}
	case *TextPart:
		return ToV1Part(*v)
	case DataPart:
		return &protocol.Part{Content: protocol.Data{Value: v.Data}, Metadata: v.Metadata}
	case *DataPart:
		return ToV1Part(*v)
	case FilePart:
		return fileToV1Part(v)
	case *FilePart:
		return fileToV1Part(*v)
	default:
		log.Warnf("compat/v0: unknown part type %T, converting to data part", p)
		return &protocol.Part{Content: protocol.Data{Value: p}}
	}
}

func fileToV1Part(f FilePart) *protocol.Part {
	switch file := f.File.(type) {
	case *FileWithBytes:
		decoded, err := base64.StdEncoding.DecodeString(file.Bytes)
		if err != nil {
			// Tolerate peers that send unencoded content.
			log.Warnf("compat/v0: file part bytes are not valid base64, keeping raw: %v", err)
			decoded = []byte(file.Bytes)
		}
		return &protocol.Part{
			Content:   protocol.Raw(decoded),
			Filename:  derefString(file.Name),
			MediaType: derefString(file.MimeType),
			Metadata:  f.Metadata,
		}
	case *FileWithURI:
		return &protocol.Part{
			Content:   protocol.URL(file.URI),
			Filename:  derefString(file.Name),
			MediaType: derefString(file.MimeType),
			Metadata:  f.Metadata,
		}
	default:
		log.Warnf("compat/v0: unknown file union type %T, converting to data part", file)
		return &protocol.Part{Content: protocol.Data{Value: file}, Metadata: f.Metadata}
	}
}

// FromV1Part converts a v1 unified part to a legacy kind-discriminated part.
func FromV1Part(p *protocol.Part) Part {
	if p == nil {
		return nil
	}
	switch c := p.Content.(type) {
	case protocol.Text:
		return TextPart{Kind: KindText, Text: string(c), Metadata: p.Metadata}
	case protocol.Data:
		return DataPart{Kind: KindData, Data: c.Value, Metadata: p.Metadata}
	case protocol.Raw:
		return FilePart{
			Kind: KindFile,
			File: &FileWithBytes{
				Name:     optString(p.Filename),
				MimeType: optString(p.MediaType),
				Bytes:    base64.StdEncoding.EncodeToString(c),
			},
			Metadata: p.Metadata,
		}
	case protocol.URL:
		return FilePart{
			Kind: KindFile,
			File: &FileWithURI{
				Name:     optString(p.Filename),
				MimeType: optString(p.MediaType),
				URI:      string(c),
			},
			Metadata: p.Metadata,
		}
	default:
		log.Warnf("compat/v0: unknown v1 part content %T, converting to data part", c)
		return DataPart{Kind: KindData, Data: p.Content, Metadata: p.Metadata}
	}
}

// ToV1Parts converts a slice of legacy parts.
func ToV1Parts(parts []Part) []*protocol.Part {
	if parts == nil {
		return nil
	}
	res := make([]*protocol.Part, 0, len(parts))
	for _, p := range parts {
		res = append(res, ToV1Part(p))
	}
	return res
}

// FromV1Parts converts a slice of v1 parts.
func FromV1Parts(parts []*protocol.Part) []Part {
	if parts == nil {
		return nil
	}
	res := make([]Part, 0, len(parts))
	for _, p := range parts {
		res = append(res, FromV1Part(p))
	}
	return res
}

// ---------------------------------------------------------------------------
// Message / Artifact / Task
// ---------------------------------------------------------------------------

// ToV1Message converts a legacy message to a v1 message.
func ToV1Message(m *Message) *protocol.Message {
	if m == nil {
		return nil
	}
	return &protocol.Message{
		ContextID:        m.ContextID,
		Extensions:       m.Extensions,
		MessageID:        m.MessageID,
		Metadata:         m.Metadata,
		Parts:            ToV1Parts(m.Parts),
		ReferenceTaskIDs: m.ReferenceTaskIDs,
		Role:             ToV1Role(m.Role),
		TaskID:           m.TaskID,
	}
}

// FromV1Message converts a v1 message to a legacy message.
func FromV1Message(m *protocol.Message) *Message {
	if m == nil {
		return nil
	}
	return &Message{
		ContextID:        m.ContextID,
		Extensions:       m.Extensions,
		Kind:             KindMessage,
		MessageID:        m.MessageID,
		Metadata:         m.Metadata,
		Parts:            FromV1Parts(m.Parts),
		ReferenceTaskIDs: m.ReferenceTaskIDs,
		Role:             FromV1Role(m.Role),
		TaskID:           m.TaskID,
	}
}

// ToV1Artifact converts a legacy artifact to a v1 artifact.
func ToV1Artifact(a Artifact) protocol.Artifact {
	return protocol.Artifact{
		ArtifactID:  a.ArtifactID,
		Name:        a.Name,
		Description: a.Description,
		Parts:       ToV1Parts(a.Parts),
		Metadata:    a.Metadata,
		Extensions:  a.Extensions,
	}
}

// FromV1Artifact converts a v1 artifact to a legacy artifact.
func FromV1Artifact(a protocol.Artifact) Artifact {
	return Artifact{
		ArtifactID:  a.ArtifactID,
		Name:        a.Name,
		Description: a.Description,
		Parts:       FromV1Parts(a.Parts),
		Metadata:    a.Metadata,
		Extensions:  a.Extensions,
	}
}

// ToV1TaskStatus converts a legacy task status to a v1 task status.
func ToV1TaskStatus(s TaskStatus) protocol.TaskStatus {
	return protocol.TaskStatus{
		State:     ToV1TaskState(s.State),
		Message:   ToV1Message(s.Message),
		Timestamp: s.Timestamp,
	}
}

// FromV1TaskStatus converts a v1 task status to a legacy task status.
func FromV1TaskStatus(s protocol.TaskStatus) TaskStatus {
	return TaskStatus{
		State:     FromV1TaskState(s.State),
		Message:   FromV1Message(s.Message),
		Timestamp: s.Timestamp,
	}
}

// ToV1Task converts a legacy task to a v1 task.
func ToV1Task(t *Task) *protocol.Task {
	if t == nil {
		return nil
	}
	res := &protocol.Task{
		ID:        t.ID,
		ContextID: t.ContextID,
		Status:    ToV1TaskStatus(t.Status),
		Metadata:  t.Metadata,
	}
	for _, a := range t.Artifacts {
		res.Artifacts = append(res.Artifacts, ToV1Artifact(a))
	}
	for i := range t.History {
		res.History = append(res.History, *ToV1Message(&t.History[i]))
	}
	return res
}

// FromV1Task converts a v1 task to a legacy task.
func FromV1Task(t *protocol.Task) *Task {
	if t == nil {
		return nil
	}
	res := &Task{
		ID:        t.ID,
		ContextID: t.ContextID,
		Kind:      KindTask,
		Status:    FromV1TaskStatus(t.Status),
		Metadata:  t.Metadata,
	}
	for _, a := range t.Artifacts {
		res.Artifacts = append(res.Artifacts, FromV1Artifact(a))
	}
	for i := range t.History {
		res.History = append(res.History, *FromV1Message(&t.History[i]))
	}
	return res
}

// ---------------------------------------------------------------------------
// Stream events
// ---------------------------------------------------------------------------

// ToV1StatusUpdate converts a legacy status update event to its v1 form.
func ToV1StatusUpdate(e *TaskStatusUpdateEvent) *protocol.TaskStatusUpdateEvent {
	if e == nil {
		return nil
	}
	return &protocol.TaskStatusUpdateEvent{
		TaskID:    e.TaskID,
		ContextID: e.ContextID,
		Status:    ToV1TaskStatus(e.Status),
		Final:     e.Final,
		Metadata:  e.Metadata,
	}
}

// FromV1StatusUpdate converts a v1 status update event to its legacy form.
// The legacy "final" flag is derived from the v1 event (terminal or
// input-required state), since v1 no longer carries it on the wire.
func FromV1StatusUpdate(e *protocol.TaskStatusUpdateEvent) *TaskStatusUpdateEvent {
	if e == nil {
		return nil
	}
	return &TaskStatusUpdateEvent{
		TaskID:    e.TaskID,
		ContextID: e.ContextID,
		Kind:      KindTaskStatusUpdate,
		Status:    FromV1TaskStatus(e.Status),
		Final:     e.IsFinal(),
		Metadata:  e.Metadata,
	}
}

// ToV1ArtifactUpdate converts a legacy artifact update event to its v1 form.
func ToV1ArtifactUpdate(e *TaskArtifactUpdateEvent) *protocol.TaskArtifactUpdateEvent {
	if e == nil {
		return nil
	}
	return &protocol.TaskArtifactUpdateEvent{
		TaskID:    e.TaskID,
		ContextID: e.ContextID,
		Artifact:  ToV1Artifact(e.Artifact),
		Append:    e.Append,
		LastChunk: e.LastChunk,
		Metadata:  e.Metadata,
	}
}

// FromV1ArtifactUpdate converts a v1 artifact update event to its legacy form.
func FromV1ArtifactUpdate(e *protocol.TaskArtifactUpdateEvent) *TaskArtifactUpdateEvent {
	if e == nil {
		return nil
	}
	return &TaskArtifactUpdateEvent{
		TaskID:    e.TaskID,
		ContextID: e.ContextID,
		Kind:      KindTaskArtifactUpdate,
		Artifact:  FromV1Artifact(e.Artifact),
		Append:    e.Append,
		LastChunk: e.LastChunk,
		Metadata:  e.Metadata,
	}
}

// ToV1StreamResponse converts a legacy streaming event to a v1 stream response.
func ToV1StreamResponse(ev StreamingMessageEvent) (protocol.StreamResponse, error) {
	switch v := ev.Result.(type) {
	case *Message:
		return protocol.StreamResponse{Result: ToV1Message(v)}, nil
	case *Task:
		return protocol.StreamResponse{Result: ToV1Task(v)}, nil
	case *TaskStatusUpdateEvent:
		return protocol.StreamResponse{Result: ToV1StatusUpdate(v)}, nil
	case *TaskArtifactUpdateEvent:
		return protocol.StreamResponse{Result: ToV1ArtifactUpdate(v)}, nil
	default:
		return protocol.StreamResponse{}, fmt.Errorf("compat/v0: unknown streaming result type %T", ev.Result)
	}
}

// FromV1StreamResponse converts a v1 stream response to a legacy streaming event.
func FromV1StreamResponse(r protocol.StreamResponse) (StreamingMessageEvent, error) {
	switch {
	case r.GetMessage() != nil:
		return StreamingMessageEvent{Result: FromV1Message(r.GetMessage())}, nil
	case r.GetTask() != nil:
		return StreamingMessageEvent{Result: FromV1Task(r.GetTask())}, nil
	case r.GetStatusUpdate() != nil:
		return StreamingMessageEvent{Result: FromV1StatusUpdate(r.GetStatusUpdate())}, nil
	case r.GetArtifactUpdate() != nil:
		return StreamingMessageEvent{Result: FromV1ArtifactUpdate(r.GetArtifactUpdate())}, nil
	default:
		return StreamingMessageEvent{}, fmt.Errorf("compat/v0: empty v1 stream response")
	}
}

// ToV1SendMessageResponse converts a legacy unary result to a v1 response.
func ToV1SendMessageResponse(r *MessageResult) (*protocol.SendMessageResponse, error) {
	if r == nil {
		return nil, nil
	}
	switch v := r.Result.(type) {
	case *Message:
		return &protocol.SendMessageResponse{Result: ToV1Message(v)}, nil
	case *Task:
		return &protocol.SendMessageResponse{Result: ToV1Task(v)}, nil
	default:
		return nil, fmt.Errorf("compat/v0: unknown unary result type %T", r.Result)
	}
}

// FromV1SendMessageResponse converts a v1 response to a legacy unary result.
func FromV1SendMessageResponse(r *protocol.SendMessageResponse) (*MessageResult, error) {
	if r == nil {
		return nil, nil
	}
	switch {
	case r.GetMessage() != nil:
		return &MessageResult{Result: FromV1Message(r.GetMessage())}, nil
	case r.GetTask() != nil:
		return &MessageResult{Result: FromV1Task(r.GetTask())}, nil
	default:
		return nil, fmt.Errorf("compat/v0: empty v1 send message response")
	}
}

// ---------------------------------------------------------------------------
// Request parameters
// ---------------------------------------------------------------------------

// ToV1SendMessageParams converts legacy send params to v1 params.
// The legacy "blocking" flag maps to v1 "returnImmediately" with inverted
// semantics (v0 default blocking=false == v1 returnImmediately=true is NOT
// the mapping: v0 blocking unset means non-blocking, which is
// returnImmediately=true).
func ToV1SendMessageParams(p SendMessageParams) protocol.SendMessageParams {
	res := protocol.SendMessageParams{
		RPCID:    p.RPCID,
		Message:  *ToV1Message(&p.Message),
		Metadata: p.Metadata,
	}
	if p.Configuration != nil {
		cfg := &protocol.SendMessageConfiguration{
			AcceptedOutputModes: p.Configuration.AcceptedOutputModes,
			HistoryLength:       p.Configuration.HistoryLength,
		}
		// blocking=true -> wait for completion -> returnImmediately=false.
		returnImmediately := !(p.Configuration.Blocking != nil && *p.Configuration.Blocking)
		cfg.ReturnImmediately = &returnImmediately
		if p.Configuration.PushNotificationConfig != nil {
			flat := toV1PushDetails(*p.Configuration.PushNotificationConfig, "")
			cfg.PushConfig = &flat
		}
		res.Configuration = cfg
	}
	return res
}

// FromV1SendMessageParams converts v1 send params to legacy params.
func FromV1SendMessageParams(p protocol.SendMessageParams) SendMessageParams {
	res := SendMessageParams{
		RPCID:    p.RPCID,
		Message:  *FromV1Message(&p.Message),
		Metadata: p.Metadata,
	}
	if p.Configuration != nil {
		blocking := !(p.Configuration.ReturnImmediately != nil && *p.Configuration.ReturnImmediately)
		cfg := &SendMessageConfiguration{
			AcceptedOutputModes: p.Configuration.AcceptedOutputModes,
			HistoryLength:       p.Configuration.HistoryLength,
			Blocking:            &blocking,
		}
		if p.Configuration.PushConfig != nil {
			nested := fromV1PushDetails(*p.Configuration.PushConfig)
			cfg.PushNotificationConfig = &nested
		}
		res.Configuration = cfg
	}
	return res
}

// ToV1TaskQueryParams converts legacy task query params to v1 params.
func ToV1TaskQueryParams(p TaskQueryParams) protocol.TaskQueryParams {
	return protocol.TaskQueryParams{
		RPCID:         p.RPCID,
		ID:            p.ID,
		HistoryLength: p.HistoryLength,
		Metadata:      p.Metadata,
	}
}

// ToV1TaskIDParams converts legacy task ID params to v1 params.
func ToV1TaskIDParams(p TaskIDParams) protocol.TaskIDParams {
	return protocol.TaskIDParams{
		RPCID:    p.RPCID,
		ID:       p.ID,
		Metadata: p.Metadata,
	}
}

// ---------------------------------------------------------------------------
// Push notification config
// ---------------------------------------------------------------------------

// ToV1PushConfig converts a legacy (nested) push config to the v1 flat shape.
func ToV1PushConfig(c TaskPushNotificationConfig) protocol.TaskPushNotificationConfig {
	flat := toV1PushDetails(c.PushNotificationConfig, c.TaskID)
	flat.RPCID = c.RPCID
	if flat.Metadata == nil {
		flat.Metadata = c.Metadata
	}
	return flat
}

// FromV1PushConfig converts a v1 flat push config to the legacy nested shape.
func FromV1PushConfig(c protocol.TaskPushNotificationConfig) TaskPushNotificationConfig {
	return TaskPushNotificationConfig{
		RPCID:                  c.RPCID,
		TaskID:                 c.TaskID,
		PushNotificationConfig: fromV1PushDetails(c),
		Metadata:               c.Metadata,
	}
}

func toV1PushDetails(c PushNotificationConfig, taskID string) protocol.TaskPushNotificationConfig {
	res := protocol.TaskPushNotificationConfig{
		ID:       c.ID,
		TaskID:   taskID,
		URL:      c.URL,
		Token:    c.Token,
		Metadata: c.Metadata,
	}
	if c.Authentication != nil {
		auth := &protocol.AuthenticationInfo{
			Credentials: derefString(c.Authentication.Credentials),
		}
		// v0 carries a list of schemes; v1 carries a single scheme.
		if len(c.Authentication.Schemes) > 0 {
			auth.Scheme = c.Authentication.Schemes[0]
			if len(c.Authentication.Schemes) > 1 {
				log.Warnf("compat/v0: dropping extra push auth schemes %v", c.Authentication.Schemes[1:])
			}
		}
		res.Authentication = auth
	}
	return res
}

func fromV1PushDetails(c protocol.TaskPushNotificationConfig) PushNotificationConfig {
	res := PushNotificationConfig{
		ID:       c.ID,
		URL:      c.URL,
		Token:    c.Token,
		Metadata: c.Metadata,
	}
	if c.Authentication != nil {
		auth := &PushNotificationAuthenticationInfo{
			Schemes: []string{c.Authentication.Scheme},
		}
		if c.Authentication.Credentials != "" {
			auth.Credentials = optString(c.Authentication.Credentials)
		}
		res.Authentication = auth
	}
	return res
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
