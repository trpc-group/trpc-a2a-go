// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Version is the legacy protocol version this compatibility layer emulates.
const Version = "0.2.5"

// Legacy v0.2.x JSON-RPC method names (slash-delimited). They are disjoint
// from the v1.0 PascalCase names, so both generations can be dispatched on a
// single endpoint.
const (
	MethodMessageSend                    = "message/send"
	MethodMessageStream                  = "message/stream"
	MethodTasksGet                       = "tasks/get"
	MethodTasksCancel                    = "tasks/cancel"
	MethodTasksPushNotificationConfigSet = "tasks/pushNotificationConfig/set"
	MethodTasksPushNotificationConfigGet = "tasks/pushNotificationConfig/get"
	MethodTasksResubscribe               = "tasks/resubscribe"
	MethodAgentAuthenticatedExtendedCard = "agent/getAuthenticatedExtendedCard"
)

// Legacy SSE event type strings (v1.0 renamed these to statusUpdate /
// artifactUpdate).
const (
	EventStatusUpdate   = "task_status_update"
	EventArtifactUpdate = "task_artifact_update"
	EventTask           = "task"
	EventMessage        = "message"
)

// Handler is an http.Handler that serves the legacy v0.2.x JSON-RPC wire
// protocol on top of a v1 TaskManager. Requests are parsed into the frozen
// legacy types, translated to v1, executed by the task manager, and the
// results are translated back to the legacy wire format. The task manager
// never sees v0 types.
type Handler struct {
	tm taskmanager.TaskManager
	// extendedCardGetter optionally serves agent/getAuthenticatedExtendedCard.
	extendedCardGetter func(ctx context.Context) (*protocol.AgentCard, error)
}

// HandlerOption configures a Handler.
type HandlerOption func(*Handler)

// WithExtendedAgentCard enables the legacy agent/getAuthenticatedExtendedCard
// method, serving the card returned by getter (legacy fields are filled in
// automatically so old clients can parse it).
func WithExtendedAgentCard(getter func(ctx context.Context) (*protocol.AgentCard, error)) HandlerOption {
	return func(h *Handler) { h.extendedCardGetter = getter }
}

// NewJSONRPCHandler creates a legacy-protocol handler backed by the given v1
// task manager.
func NewJSONRPCHandler(tm taskmanager.TaskManager, opts ...HandlerOption) *Handler {
	h := &Handler{tm: tm}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// IsLegacyMethod reports whether the JSON-RPC method name belongs to the
// legacy v0.2.x protocol (slash-delimited names).
func IsLegacyMethod(method string) bool {
	return strings.Contains(method, "/")
}

// ServeHTTP handles a legacy JSON-RPC request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, nil, jsonrpc.ErrInvalidRequest("only POST is supported"))
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.Errorf("compat/v0: failed to close request body: %v", err)
		}
	}()

	var req jsonrpc.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, jsonrpc.ErrParseError(fmt.Sprintf("failed to parse request: %v", err)))
		return
	}
	if req.JSONRPC != jsonrpc.Version {
		writeError(w, req.ID, jsonrpc.ErrInvalidRequest("invalid jsonrpc version"))
		return
	}

	ctx := r.Context()
	switch req.Method {
	case MethodMessageSend:
		h.handleMessageSend(ctx, w, &req)
	case MethodMessageStream:
		h.handleStreaming(ctx, w, r, &req, false)
	case MethodTasksGet:
		h.handleTasksGet(ctx, w, &req)
	case MethodTasksCancel:
		h.handleTasksCancel(ctx, w, &req)
	case MethodTasksResubscribe:
		h.handleStreaming(ctx, w, r, &req, true)
	case MethodTasksPushNotificationConfigSet:
		h.handlePushSet(ctx, w, &req)
	case MethodTasksPushNotificationConfigGet:
		h.handlePushGet(ctx, w, &req)
	case MethodAgentAuthenticatedExtendedCard:
		h.handleExtendedCard(ctx, w, &req)
	default:
		writeError(w, req.ID,
			jsonrpc.ErrMethodNotFound(fmt.Sprintf("method '%s' not supported", req.Method)))
	}
}

func (h *Handler) handleMessageSend(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	var params SendMessageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
		return
	}
	resp, err := h.tm.OnSendMessage(ctx, ToV1SendMessageParams(params))
	if err != nil {
		writeTaskManagerError(w, req.ID, err, "OnSendMessage")
		return
	}
	legacy, err := FromV1SendMessageResponse(resp)
	if err != nil {
		writeError(w, req.ID, jsonrpc.ErrInternalError(err.Error()))
		return
	}
	writeResult(w, req.ID, legacy)
}

func (h *Handler) handleTasksGet(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	var params TaskQueryParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
		return
	}
	task, err := h.tm.OnGetTask(ctx, ToV1TaskQueryParams(params))
	if err != nil {
		writeTaskManagerError(w, req.ID, err, "OnGetTask")
		return
	}
	writeResult(w, req.ID, FromV1Task(task))
}

func (h *Handler) handleTasksCancel(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	var params TaskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
		return
	}
	task, err := h.tm.OnCancelTask(ctx, ToV1TaskIDParams(params))
	if err != nil {
		writeTaskManagerError(w, req.ID, err, "OnCancelTask")
		return
	}
	writeResult(w, req.ID, FromV1Task(task))
}

func (h *Handler) handlePushSet(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	var params TaskPushNotificationConfig
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
		return
	}
	if params.TaskID == "" {
		writeError(w, req.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	config, err := h.tm.OnPushNotificationSet(ctx, ToV1PushConfig(params))
	if err != nil {
		writeTaskManagerError(w, req.ID, err, "OnPushNotificationSet")
		return
	}
	legacy := FromV1PushConfig(*config)
	writeResult(w, req.ID, &legacy)
}

func (h *Handler) handlePushGet(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	var params TaskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
		return
	}
	config, err := h.tm.OnPushNotificationGet(ctx, ToV1TaskIDParams(params))
	if err != nil {
		writeTaskManagerError(w, req.ID, err, "OnPushNotificationGet")
		return
	}
	legacy := FromV1PushConfig(*config)
	writeResult(w, req.ID, &legacy)
}

func (h *Handler) handleExtendedCard(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	if h.extendedCardGetter == nil {
		writeError(w, req.ID,
			jsonrpc.ErrMethodNotFound("authenticated extended card not configured"))
		return
	}
	card, err := h.extendedCardGetter(ctx)
	if err != nil {
		writeError(w, req.ID, jsonrpc.ErrInternalError(err.Error()))
		return
	}
	legacyCard := *card
	FillLegacyCardFields(&legacyCard)
	writeResult(w, req.ID, &legacyCard)
}

// handleStreaming serves message/stream and tasks/resubscribe over SSE using
// the legacy wire format: event names task_status_update/task_artifact_update/
// task/message, and each data payload is a JSON-RPC response whose result is
// the kind-discriminated legacy event. The stream ends by closing the
// connection (matching the legacy server behavior).
func (h *Handler) handleStreaming(
	ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, resubscribe bool,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, req.ID, jsonrpc.ErrInternalError("streaming unsupported by server"))
		return
	}

	var events <-chan protocol.StreamResponse
	if resubscribe {
		var params TaskIDParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
			return
		}
		ch, err := h.tm.OnResubscribe(ctx, ToV1TaskIDParams(params))
		if err != nil {
			writeTaskManagerError(w, req.ID, err, "OnResubscribe")
			return
		}
		events = ch
	} else {
		var params SendMessageParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeError(w, req.ID, jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err)))
			return
		}
		ch, err := h.tm.OnSendMessageStream(ctx, ToV1SendMessageParams(params))
		if err != nil {
			writeTaskManagerError(w, req.ID, err, "OnSendMessageStream")
			return
		}
		events = ch
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return // Channel closed: stream ends by closing the connection.
			}
			legacy, err := FromV1StreamResponse(event)
			if err != nil {
				log.Warnf("compat/v0: skipping unconvertible stream event: %v", err)
				continue
			}
			batch := []sse.EventBatch{{
				EventType: legacyEventType(legacy),
				ID:        req.ID,
				Data:      &legacy,
			}}
			if err := sse.FormatJSONRPCEventBatch(w, batch); err != nil {
				log.Errorf("compat/v0: failed to write SSE event (client likely disconnected): %v", err)
				return
			}
			flusher.Flush()
		}
	}
}

func legacyEventType(ev StreamingMessageEvent) string {
	switch ev.Result.(type) {
	case *TaskStatusUpdateEvent:
		return EventStatusUpdate
	case *TaskArtifactUpdateEvent:
		return EventArtifactUpdate
	case *Task:
		return EventTask
	case *Message:
		return EventMessage
	default:
		return EventMessage
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC response writers (mirroring the v1 server behavior)
// ---------------------------------------------------------------------------

func writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(jsonrpc.NewResponse(id, result)); err != nil {
		log.Errorf("compat/v0: failed to write JSON-RPC response (ID: %v): %v", id, err)
	}
}

func writeError(w http.ResponseWriter, id interface{}, rpcErr *jsonrpc.Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	httpStatus := http.StatusInternalServerError
	switch rpcErr.Code {
	case jsonrpc.CodeParseError, jsonrpc.CodeInvalidRequest, jsonrpc.CodeInvalidParams:
		httpStatus = http.StatusBadRequest
	case jsonrpc.CodeMethodNotFound:
		httpStatus = http.StatusNotFound
	}
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(jsonrpc.NewErrorResponse(id, rpcErr)); err != nil {
		log.Errorf("compat/v0: failed to write JSON-RPC error response (ID: %v): %v", id, err)
	}
}

func writeTaskManagerError(w http.ResponseWriter, id interface{}, err error, operation string) {
	if rpcErr, ok := err.(*jsonrpc.Error); ok {
		log.Errorf("compat/v0: error calling %s: %v", operation, rpcErr)
		writeError(w, id, rpcErr)
		return
	}
	log.Errorf("compat/v0: unexpected error calling %s: %v", operation, err)
	writeError(w, id, jsonrpc.ErrInternalError(fmt.Sprintf("%s failed: %v", operation, err)))
}

// ---------------------------------------------------------------------------
// Dual-protocol dispatch
// ---------------------------------------------------------------------------

// NewDualHandler returns an http.Handler that serves both protocol
// generations on a single endpoint: requests using legacy slash-delimited
// method names (message/send, tasks/get, ...) are routed to the v0
// compatibility handler, everything else (v1.0 PascalCase names, GET
// requests, malformed bodies) falls through to the v1 handler. This works
// because the two method-name sets are disjoint.
func NewDualHandler(v1Handler http.Handler, v0Handler *Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			v1Handler.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if closeErr := r.Body.Close(); closeErr != nil {
			log.Errorf("compat/v0: failed to close request body: %v", closeErr)
		}
		if err != nil {
			v1Handler.ServeHTTP(w, r)
			return
		}
		// Restore the body for the downstream handler.
		r.Body = io.NopCloser(bytes.NewReader(body))

		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &probe) == nil && IsLegacyMethod(probe.Method) {
			v0Handler.ServeHTTP(w, r)
			return
		}
		v1Handler.ServeHTTP(w, r)
	})
}
