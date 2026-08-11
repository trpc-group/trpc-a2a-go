// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

type httpJSONRoute struct {
	operation string
	tenant    string
	taskID    string
	configID  string
}

const defaultHTTPJSONMaxBodyBytes int64 = 4 << 20

func normalizeHTTPJSONBasePath(basePath string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" || basePath == "/" {
		return ""
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	return strings.TrimSuffix(basePath, "/")
}

func httpJSONServeMuxPattern(basePath string) string {
	basePath = normalizeHTTPJSONBasePath(basePath)
	if basePath == "" {
		return "/"
	}
	return basePath + "/"
}

// parseHTTPJSONRoute maps an escaped request path to an HTTP+JSON operation.
//
// requestPath must be url.URL.EscapedPath() (still percent-encoded). Matching
// runs on escaped segments so a literal "%3A" is not treated as the operation
// verb colon: "/tasks/job%3Acancel" is GetTask("job:cancel"), while
// "/tasks/job:cancel" is Cancel("job"). Identifiers are PathUnescape'd only
// after their position in the route is known.
//
// Layout after stripping basePath:
//
//	/{op}...                 — no tenant (common case)
//	/{tenant}/{op}...        — tenant prefix from a2a.proto's additional binding
//
// Try the tenant-less parse first. The only ambiguous form is a GetTask whose
// raw ID segment is also a top-level operation: it may instead be the official
// tenant="tasks" additional binding. Literal operation segments select that
// binding for interoperability with reference clients; percent-encoding one
// character keeps the corresponding task ID addressable.
func parseHTTPJSONRoute(requestPath, basePath string) (httpJSONRoute, bool) {
	// Strip the configured mount prefix (also compared in escaped form).
	basePath = normalizeHTTPJSONBasePath(basePath)
	escapedBasePath := (&url.URL{Path: basePath}).EscapedPath()
	if escapedBasePath != "" {
		if !strings.HasPrefix(requestPath, escapedBasePath+"/") {
			return httpJSONRoute{}, false
		}
		requestPath = strings.TrimPrefix(requestPath, escapedBasePath)
	}

	segments := strings.Split(strings.Trim(requestPath, "/"), "/")
	for _, segment := range segments {
		// Reject "//" or a trailing empty segment after the split.
		if segment == "" {
			return httpJSONRoute{}, false
		}
	}

	// 1) Tenant-less: /message:send, /tasks/{id}, /tasks/{id}:cancel, ...
	route, ok := matchHTTPJSONRouteSegments(segments, "")
	if ok && !shadowsTasksTenant(segments, route) {
		return route, true
	}

	// 2) Tenant-prefixed: /{tenant}/message:send, /{tenant}/tasks/{id}, ...
	if len(segments) > 1 {
		tenant, err := url.PathUnescape(segments[0])
		if err != nil || tenant == "" {
			return httpJSONRoute{}, false
		}
		if prefixed, prefixedOK := matchHTTPJSONRouteSegments(segments[1:], tenant); prefixedOK {
			return prefixed, true
		}
	}

	return route, ok
}

// shadowsTasksTenant reports whether /tasks/{segment} is also a valid
// tenant="tasks" operation. Compare the raw segment so an escaped task ID is
// not mistaken for the additional tenant binding.
func shadowsTasksTenant(segments []string, route httpJSONRoute) bool {
	if route.operation != protocol.MethodTasksGet || len(segments) != 2 {
		return false
	}
	switch segments[1] {
	case "message:send", "message:stream", "tasks", "extendedAgentCard":
		return true
	default:
		return false
	}
}

// unescapeSegment decodes one path segment into an identifier.
func unescapeSegment(segment string) (string, bool) {
	decoded, err := url.PathUnescape(segment)
	if err != nil || decoded == "" {
		return "", false
	}
	return decoded, true
}

func matchHTTPJSONRouteSegments(segments []string, tenant string) (httpJSONRoute, bool) {
	if len(segments) == 1 {
		switch segments[0] {
		case "message:send":
			return httpJSONRoute{operation: protocol.MethodMessageSend, tenant: tenant}, true
		case "message:stream":
			return httpJSONRoute{operation: protocol.MethodMessageStream, tenant: tenant}, true
		case "tasks":
			return httpJSONRoute{operation: protocol.MethodTasksList, tenant: tenant}, true
		case "extendedAgentCard":
			return httpJSONRoute{operation: protocol.MethodAgentAuthenticatedExtendedCard, tenant: tenant}, true
		}
	}
	if len(segments) < 2 || segments[0] != "tasks" {
		return httpJSONRoute{}, false
	}
	if len(segments) == 2 {
		// The verb is matched on the escaped segment, so only a literal colon
		// separates it from the task ID.
		for verb, operation := range map[string]string{
			":cancel":    protocol.MethodTasksCancel,
			":subscribe": protocol.MethodTasksResubscribe,
		} {
			if !strings.HasSuffix(segments[1], verb) {
				continue
			}
			taskID, ok := unescapeSegment(strings.TrimSuffix(segments[1], verb))
			if !ok {
				return httpJSONRoute{}, false
			}
			return httpJSONRoute{operation: operation, tenant: tenant, taskID: taskID}, true
		}
		taskID, ok := unescapeSegment(segments[1])
		if !ok {
			return httpJSONRoute{}, false
		}
		return httpJSONRoute{operation: protocol.MethodTasksGet, tenant: tenant, taskID: taskID}, true
	}
	if segments[2] != "pushNotificationConfigs" {
		return httpJSONRoute{}, false
	}
	taskID, ok := unescapeSegment(segments[1])
	if !ok {
		return httpJSONRoute{}, false
	}
	if len(segments) == 3 {
		return httpJSONRoute{
			operation: protocol.MethodTasksPushNotificationConfigList,
			tenant:    tenant,
			taskID:    taskID,
		}, true
	}
	if len(segments) == 4 {
		configID, configOK := unescapeSegment(segments[3])
		if !configOK {
			return httpJSONRoute{}, false
		}
		return httpJSONRoute{
			operation: protocol.MethodTasksPushNotificationConfigGet,
			tenant:    tenant,
			taskID:    taskID,
			configID:  configID,
		}, true
	}
	return httpJSONRoute{}, false
}

func (s *A2AServer) handleHTTPJSON(w http.ResponseWriter, r *http.Request) {
	if s.corsEnabled {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	route, ok := parseHTTPJSONRoute(r.URL.EscapedPath(), s.httpJSONBasePath)
	if !ok {
		s.writeHTTPJSONStatus(w, http.StatusNotFound, "NOT_FOUND", "HTTP+JSON endpoint not found")
		return
	}
	if !httpJSONMethodAllowed(route.operation, r.Method) {
		w.Header().Set("Allow", strings.Join(httpJSONAllowedMethods(route.operation), ", "))
		s.writeHTTPJSONStatus(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "HTTP method not allowed")
		return
	}
	requestedVersion := r.Header.Get("A2A-Version")
	if requestedVersion == "" {
		requestedVersion = r.URL.Query().Get("A2A-Version")
	}
	if requestedVersion != "" && requestedVersion != protocol.ProtocolVersionV1 {
		s.writeHTTPJSONError(w, taskmanager.ErrVersionNotSupported(requestedVersion), route.taskID)
		return
	}
	if err := validateHTTPJSONContentType(r); err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}

	switch route.operation {
	case protocol.MethodMessageSend:
		s.handleHTTPJSONMessageSend(w, r, route)
	case protocol.MethodMessageStream:
		s.handleHTTPJSONMessageStream(w, r, route)
	case protocol.MethodTasksGet:
		s.handleHTTPJSONTaskGet(w, r, route)
	case protocol.MethodTasksList:
		s.handleHTTPJSONTasksList(w, r, route)
	case protocol.MethodTasksCancel:
		s.handleHTTPJSONTaskCancel(w, r, route)
	case protocol.MethodTasksResubscribe:
		s.handleHTTPJSONTaskSubscribe(w, r, route)
	case protocol.MethodTasksPushNotificationConfigList:
		if r.Method == http.MethodPost {
			s.handleHTTPJSONPushConfigCreate(w, r, route)
		} else {
			s.handleHTTPJSONPushConfigList(w, r, route)
		}
	case protocol.MethodTasksPushNotificationConfigGet:
		if r.Method == http.MethodDelete {
			s.handleHTTPJSONPushConfigDelete(w, r, route)
		} else {
			s.handleHTTPJSONPushConfigGet(w, r, route)
		}
	case protocol.MethodAgentAuthenticatedExtendedCard:
		s.handleHTTPJSONExtendedAgentCard(w, r, route)
	default:
		s.writeHTTPJSONStatus(w, http.StatusNotFound, "NOT_FOUND", "HTTP+JSON endpoint not found")
	}
}

func httpJSONAllowedMethods(operation string) []string {
	switch operation {
	case protocol.MethodMessageSend, protocol.MethodMessageStream, protocol.MethodTasksCancel:
		return []string{http.MethodPost}
	case protocol.MethodTasksResubscribe:
		return []string{http.MethodGet, http.MethodPost}
	case protocol.MethodTasksPushNotificationConfigList:
		return []string{http.MethodGet, http.MethodPost}
	case protocol.MethodTasksPushNotificationConfigGet:
		return []string{http.MethodGet, http.MethodDelete}
	default:
		return []string{http.MethodGet}
	}
}

func httpJSONMethodAllowed(operation, method string) bool {
	for _, allowed := range httpJSONAllowedMethods(operation) {
		if method == allowed {
			return true
		}
	}
	return false
}

func validateHTTPJSONContentType(r *http.Request) error {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != protocol.MediaTypeA2AJSON && mediaType != protocol.MediaTypeJSON) {
		return taskmanager.ErrContentTypeNotSupported(contentType)
	}
	return nil
}

func (s *A2AServer) decodeHTTPJSONBody(w http.ResponseWriter, r *http.Request, target any, required bool) error {
	if r.Body == nil {
		if required {
			return taskmanager.ErrInvalidParams("request body is required")
		}
		return nil
	}
	var body io.Reader = r.Body
	if s.httpJSONMaxBody > 0 {
		body = http.MaxBytesReader(w, r.Body, s.httpJSONMaxBody)
	}
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) && !required {
			return nil
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return taskmanager.ErrInvalidParams(
				fmt.Sprintf("request body exceeds %d bytes", s.httpJSONMaxBody))
		}
		return taskmanager.ErrInvalidParams(fmt.Sprintf("failed to parse request body: %v", err))
	}
	var trailing any
	err := decoder.Decode(&trailing)
	switch {
	case errors.Is(err, io.EOF):
	case err != nil:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return taskmanager.ErrInvalidParams(
				fmt.Sprintf("request body exceeds %d bytes", s.httpJSONMaxBody))
		}
		return taskmanager.ErrInvalidParams(fmt.Sprintf("failed to parse request body: %v", err))
	default:
		return taskmanager.ErrInvalidParams("request body must contain exactly one JSON value")
	}
	return nil
}

func reconcileHTTPJSONTenant(pathTenant, payloadTenant string) (string, error) {
	if pathTenant == "" {
		return payloadTenant, nil
	}
	if payloadTenant != "" && payloadTenant != pathTenant {
		return "", taskmanager.ErrInvalidParams("tenant in request does not match tenant in URL path")
	}
	return pathTenant, nil
}

func (s *A2AServer) handleHTTPJSONMessageSend(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	tracker := newMetricsTracker(protocol.MethodMessageSend, false, s.firstTokenPolicy, s.telemetryMetrics)
	defer tracker.record(r.Context())

	var params protocol.SendMessageParams
	if err := s.decodeHTTPJSONBody(w, r, &params, true); err != nil {
		tracker.setError(errTypeInvalidParams)
		s.writeHTTPJSONError(w, err, "")
		return
	}
	tenant, err := reconcileHTTPJSONTenant(route.tenant, params.Tenant)
	if err != nil {
		tracker.setError(errTypeInvalidParams)
		s.writeHTTPJSONError(w, err, "")
		return
	}
	params.Tenant = tenant
	if err := s.validateSendMessageParams(r.Context(), &params); err != nil {
		tracker.setValidateSendMessageError(err)
		s.writeHTTPJSONError(w, err, "")
		return
	}
	result, err := s.taskManager.OnSendMessage(r.Context(), params)
	if err != nil {
		tracker.setError(errTypeMessageProcessingFailed)
		taskID := ""
		if params.Message.TaskID != nil {
			taskID = *params.Message.TaskID
		}
		s.writeHTTPJSONError(w, err, taskID)
		return
	}
	tracker.observeNonStreamingResult(result)
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONMessageStream(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	tracker := newMetricsTracker(protocol.MethodMessageStream, true, s.firstTokenPolicy, s.telemetryMetrics)

	var params protocol.SendMessageParams
	if err := s.decodeHTTPJSONBody(w, r, &params, true); err != nil {
		tracker.setError(errTypeInvalidParams)
		tracker.record(r.Context())
		s.writeHTTPJSONError(w, err, "")
		return
	}
	tenant, err := reconcileHTTPJSONTenant(route.tenant, params.Tenant)
	if err != nil {
		tracker.setError(errTypeInvalidParams)
		tracker.record(r.Context())
		s.writeHTTPJSONError(w, err, "")
		return
	}
	params.Tenant = tenant
	if err := s.validateSendMessageParams(r.Context(), &params); err != nil {
		tracker.setValidateSendMessageError(err)
		tracker.record(r.Context())
		s.writeHTTPJSONError(w, err, "")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		tracker.setError(errTypeStreamingNotSupported)
		tracker.record(r.Context())
		s.writeHTTPJSONError(w, taskmanager.ErrInternalError("server does not support streaming"), "")
		return
	}
	events, err := s.taskManager.OnSendMessageStream(r.Context(), params)
	if err != nil {
		tracker.setError(errTypeSubscribeFailed)
		tracker.record(r.Context())
		taskID := ""
		if params.Message.TaskID != nil {
			taskID = *params.Message.TaskID
		}
		s.writeHTTPJSONError(w, err, taskID)
		return
	}
	streamID := params.Message.MessageID
	if params.Message.TaskID != nil {
		streamID = *params.Message.TaskID
	}
	handleSSEStream(r.Context(), s.corsEnabled, w, flusher, events, streamID, tracker, sse.FormatEventBatch)
}

func (s *A2AServer) handleHTTPJSONTaskGet(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	params := protocol.TaskQueryParams{ID: route.taskID}
	var err error
	params.Tenant, err = reconcileHTTPJSONTenant(route.tenant, r.URL.Query().Get("tenant"))
	if err == nil {
		params.HistoryLength, err = parseOptionalIntQuery(r, "historyLength")
	}
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	result, err := s.taskManager.OnGetTask(r.Context(), params)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONTasksList(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	query := r.URL.Query()
	params := protocol.ListTasksParams{
		ContextID:            query.Get("contextId"),
		Status:               protocol.TaskState(query.Get("status")),
		PageToken:            query.Get("pageToken"),
		StatusTimestampAfter: query.Get("statusTimestampAfter"),
	}
	var err error
	params.Tenant, err = reconcileHTTPJSONTenant(route.tenant, query.Get("tenant"))
	if err == nil {
		params.PageSize, err = parseOptionalIntQuery(r, "pageSize")
	}
	if err == nil {
		params.HistoryLength, err = parseOptionalIntQuery(r, "historyLength")
	}
	if err == nil {
		params.IncludeArtifacts, err = parseOptionalBoolQuery(r, "includeArtifacts")
	}
	if err != nil {
		s.writeHTTPJSONError(w, err, "")
		return
	}
	result, err := s.taskManager.OnListTasks(r.Context(), params)
	if err != nil {
		s.writeHTTPJSONError(w, err, "")
		return
	}
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONTaskCancel(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	params := protocol.TaskIDParams{}
	if err := s.decodeHTTPJSONBody(w, r, &params, false); err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	if params.ID != "" && params.ID != route.taskID {
		s.writeHTTPJSONError(w, taskmanager.ErrInvalidParams("task ID in request does not match URL path"), route.taskID)
		return
	}
	tenant, err := reconcileHTTPJSONTenant(route.tenant, params.Tenant)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	params.ID = route.taskID
	params.Tenant = tenant
	result, err := s.taskManager.OnCancelTask(r.Context(), params)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONTaskSubscribe(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	tenant, err := reconcileHTTPJSONTenant(route.tenant, r.URL.Query().Get("tenant"))
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeHTTPJSONError(w, taskmanager.ErrInternalError("server does not support streaming"), route.taskID)
		return
	}
	events, err := s.taskManager.OnResubscribe(r.Context(), protocol.TaskIDParams{ID: route.taskID, Tenant: tenant})
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	handleSSEStream(r.Context(), s.corsEnabled, w, flusher, events, route.taskID, nil, sse.FormatEventBatch)
}

func (s *A2AServer) handleHTTPJSONPushConfigCreate(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	var params protocol.TaskPushNotificationConfig
	if err := s.decodeHTTPJSONBody(w, r, &params, true); err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	if params.TaskID != "" && params.TaskID != route.taskID {
		s.writeHTTPJSONError(w, taskmanager.ErrInvalidParams("task ID in request does not match URL path"), route.taskID)
		return
	}
	tenant, err := reconcileHTTPJSONTenant(route.tenant, params.Tenant)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	params.TaskID = route.taskID
	params.Tenant = tenant
	if !s.pushAvailableForTenant(r.Context(), tenant) {
		s.writeHTTPJSONError(w, taskmanager.ErrPushNotificationNotSupported(), route.taskID)
		return
	}
	if err := push.ValidateConfig(params); err != nil {
		s.writeHTTPJSONError(w, taskmanager.ErrInvalidParams(err.Error()), route.taskID)
		return
	}
	result, err := s.taskManager.OnPushNotificationSet(r.Context(), params)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	// 200, not 201: the proto transcoding rule returns the created
	// TaskPushNotificationConfig, and the reference clients treat any non-200
	// as a failure.
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONPushConfigGet(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	tenant, err := reconcileHTTPJSONTenant(route.tenant, r.URL.Query().Get("tenant"))
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	if !s.pushAvailableForTenant(r.Context(), tenant) {
		s.writeHTTPJSONError(w, taskmanager.ErrPushNotificationNotSupported(), route.taskID)
		return
	}
	params := protocol.GetTaskPushNotificationConfigParams{
		TaskID: route.taskID,
		ID:     route.configID,
		Tenant: tenant,
	}
	result, err := s.taskManager.OnPushNotificationGet(r.Context(), params)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONPushConfigList(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	query := r.URL.Query()
	params := protocol.ListTaskPushNotificationConfigsParams{
		TaskID:    route.taskID,
		PageToken: query.Get("pageToken"),
	}
	var err error
	params.Tenant, err = reconcileHTTPJSONTenant(route.tenant, query.Get("tenant"))
	if err == nil {
		params.PageSize, err = parseOptionalIntQuery(r, "pageSize")
	}
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	if !s.pushAvailableForTenant(r.Context(), params.Tenant) {
		s.writeHTTPJSONError(w, taskmanager.ErrPushNotificationNotSupported(), route.taskID)
		return
	}
	result, err := s.taskManager.OnPushNotificationList(r.Context(), params)
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	s.writeHTTPJSONResponse(w, http.StatusOK, result)
}

func (s *A2AServer) handleHTTPJSONPushConfigDelete(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	tenant, err := reconcileHTTPJSONTenant(route.tenant, r.URL.Query().Get("tenant"))
	if err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	if !s.pushAvailableForTenant(r.Context(), tenant) {
		s.writeHTTPJSONError(w, taskmanager.ErrPushNotificationNotSupported(), route.taskID)
		return
	}
	params := protocol.DeleteTaskPushNotificationConfigParams{
		TaskID: route.taskID,
		ID:     route.configID,
		Tenant: tenant,
	}
	if err := s.taskManager.OnPushNotificationDelete(r.Context(), params); err != nil {
		s.writeHTTPJSONError(w, err, route.taskID)
		return
	}
	// DeleteTaskPushNotificationConfig returns google.protobuf.Empty, which
	// transcodes to 200 with an empty JSON object rather than 204.
	s.writeHTTPJSONResponse(w, http.StatusOK, struct{}{})
}

func (s *A2AServer) handleHTTPJSONExtendedAgentCard(w http.ResponseWriter, r *http.Request, route httpJSONRoute) {
	tenant, err := reconcileHTTPJSONTenant(route.tenant, r.URL.Query().Get("tenant"))
	if err != nil {
		s.writeHTTPJSONError(w, err, "")
		return
	}
	card, err := s.resolveExtendedAgentCard(r.Context(), tenant)
	if err != nil {
		s.writeHTTPJSONError(w, err, "")
		return
	}
	s.writeHTTPJSONResponse(w, http.StatusOK, card)
}

func (s *A2AServer) resolveExtendedAgentCard(ctx context.Context, tenant string) (AgentCard, error) {
	baseCard, ok := s.resolveAgentCard(ctx, tenant)
	if !ok {
		return AgentCard{}, taskmanager.ErrInvalidParams("unknown tenant")
	}
	if !baseCard.ExtendedAgentCardEnabled() {
		return AgentCard{}, taskmanager.ErrAuthenticatedExtendedCardNotConfigured()
	}
	if s.authenticatedCardHandler == nil {
		return s.finalizePushCapability(baseCard), nil
	}
	card, err := s.authenticatedCardHandler(ctx, baseCard)
	if err != nil {
		log.Errorf("Error applying authenticated card handler: %v", err)
		return AgentCard{}, taskmanager.ErrInternalError(fmt.Sprintf("failed to handle extended card: %v", err))
	}
	return s.finalizePushCapability(card), nil
}

func parseOptionalIntQuery(r *http.Request, name string) (*int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil, taskmanager.ErrInvalidParams(fmt.Sprintf("%s must be an integer", name))
	}
	return &parsed, nil
}

func parseOptionalBoolQuery(r *http.Request, name string) (*bool, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil, taskmanager.ErrInvalidParams(fmt.Sprintf("%s must be true or false", name))
	}
	return &parsed, nil
}

func (s *A2AServer) writeHTTPJSONResponse(w http.ResponseWriter, statusCode int, result any) {
	w.Header().Set("Content-Type", protocol.MediaTypeA2AJSON)
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Errorf("Failed to write HTTP+JSON response: %v", err)
	}
}

func (s *A2AServer) writeHTTPJSONError(w http.ResponseWriter, err error, taskID string) {
	statusCode, status, reason, message := httpJSONErrorMapping(err)
	errorBody := map[string]any{
		"code":    statusCode,
		"status":  status,
		"message": message,
	}
	if reason != "" {
		detail := map[string]any{
			"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
			"reason": reason,
			"domain": "a2a-protocol.org",
		}
		if taskID != "" {
			detail["metadata"] = map[string]string{"taskId": taskID}
		}
		errorBody["details"] = []any{detail}
	}
	s.writeHTTPJSONErrorBody(w, statusCode, map[string]any{"error": errorBody})
}

func (s *A2AServer) writeHTTPJSONStatus(w http.ResponseWriter, statusCode int, status, message string) {
	s.writeHTTPJSONErrorBody(w, statusCode, map[string]any{
		"error": map[string]any{
			"code":    statusCode,
			"status":  status,
			"message": message,
		},
	})
}

func (s *A2AServer) writeHTTPJSONErrorBody(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", protocol.MediaTypeA2AJSON)
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Errorf("Failed to write HTTP+JSON error response: %v", err)
	}
}

func httpJSONErrorMapping(err error) (int, string, string, string) {
	var taskErr *taskmanager.Error
	if !errors.As(err, &taskErr) {
		// An error a TaskManager raised outside the A2A model: log it, because
		// the opaque body below is all the client will ever see.
		log.Errorf("HTTP+JSON operation failed with a non-A2A error: %v", err)
		return http.StatusInternalServerError, "INTERNAL", "", "Internal error"
	}
	// The A2A error constructors keep the generic wording in Message and the
	// specific cause in Data. Spec §11.6 maps the human-readable string to
	// error.message, so for a client error the cause is what belongs there.
	// A server-side failure keeps the generic wording: its Data can carry a
	// database, upstream or credential detail that must not cross the wire.
	message := taskErr.Message
	detail, hasDetail := taskErr.Data.(string)
	switch taskErr.Code {
	case taskmanager.ErrCodeInternalError, taskmanager.ErrCodeInvalidAgentResponse:
		if hasDetail && detail != "" {
			log.Errorf("HTTP+JSON operation failed (%s): %s", taskErr.Code, detail)
		}
	default:
		if hasDetail && detail != "" {
			message = detail
		}
	}
	switch taskErr.Code {
	case taskmanager.ErrCodeInvalidParams:
		return http.StatusBadRequest, "INVALID_ARGUMENT", "", message
	case taskmanager.ErrCodeTaskNotFound:
		return http.StatusNotFound, "NOT_FOUND", string(taskErr.Code), message
	case taskmanager.ErrCodeTaskNotCancelable,
		taskmanager.ErrCodePushNotificationNotSupported,
		taskmanager.ErrCodeUnsupportedOperation,
		taskmanager.ErrCodeAuthenticatedExtendedCardNotConfigured,
		taskmanager.ErrCodeExtensionSupportRequired,
		taskmanager.ErrCodeVersionNotSupported:
		return http.StatusBadRequest, "FAILED_PRECONDITION", string(taskErr.Code), message
	case taskmanager.ErrCodeContentTypeNotSupported:
		return http.StatusBadRequest, "INVALID_ARGUMENT", string(taskErr.Code), message
	case taskmanager.ErrCodeInvalidAgentResponse:
		return http.StatusInternalServerError, "INTERNAL", string(taskErr.Code), message
	case taskmanager.ErrCodeInternalError:
		return http.StatusInternalServerError, "INTERNAL", "", message
	default:
		log.Errorf("HTTP+JSON operation failed with an unmapped A2A error code %s: %v", taskErr.Code, err)
		return http.StatusInternalServerError, "INTERNAL", "", "Internal error"
	}
}
