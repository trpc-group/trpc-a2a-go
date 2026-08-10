// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

type httpJSONBinding struct{}

type httpJSONRequest struct {
	method string
	path   string
	query  url.Values
	body   any
}

func (httpJSONBinding) request(
	ctx context.Context,
	client *A2AClient,
	_ string,
	operation string,
	params any,
	result any,
	opts ...RequestOption,
) error {
	spec, err := buildHTTPJSONRequest(operation, params)
	if err != nil {
		return err
	}
	req, err := client.newHTTPJSONRequest(ctx, spec, false, opts...)
	if err != nil {
		return err
	}
	resp, err := client.httpReqHandler.Handle(ctx, client.httpClient, req)
	if err != nil {
		return fmt.Errorf("HTTP+JSON request failed: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("HTTP+JSON request returned an unexpected nil response")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read HTTP+JSON response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeHTTPJSONError(resp.StatusCode, body)
	}
	if resp.StatusCode == http.StatusNoContent || result == nil {
		return nil
	}
	if err := validateHTTPJSONResponseType(resp.Header.Get("Content-Type")); err != nil {
		return err
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("failed to decode HTTP+JSON response: %w. Body: %s", err, body)
	}
	return nil
}

func (httpJSONBinding) stream(
	ctx context.Context,
	client *A2AClient,
	_ string,
	operation string,
	params any,
	opts ...RequestOption,
) (*http.Response, error) {
	spec, err := buildHTTPJSONRequest(operation, params)
	if err != nil {
		return nil, err
	}
	req, err := client.newHTTPJSONRequest(ctx, spec, true, opts...)
	if err != nil {
		return nil, err
	}
	resp, err := client.httpReqHandler.Handle(ctx, client.httpClient, req)
	if err != nil {
		return nil, fmt.Errorf("HTTP+JSON stream request failed: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("HTTP+JSON stream request returned an unexpected nil response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, decodeHTTPJSONError(resp.StatusCode, body)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != protocol.MediaTypeEventStream {
		resp.Body.Close()
		return nil, fmt.Errorf(
			"server did not respond with Content-Type %q, got %q",
			protocol.MediaTypeEventStream,
			resp.Header.Get("Content-Type"),
		)
	}
	return resp, nil
}

func (httpJSONBinding) decodeSSEData(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("HTTP+JSON SSE event data is empty")
	}
	return data, nil
}

//nolint:gocyclo // Keeping the protocol operation-to-route mapping in one switch makes completeness auditable.
func buildHTTPJSONRequest(operation string, params any) (httpJSONRequest, error) {
	switch operation {
	case protocol.MethodMessageSend, protocol.MethodMessageStream:
		p, ok := params.(protocol.SendMessageParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid %s parameters %T", operation, params)
		}
		suffix := "/message:send"
		if operation == protocol.MethodMessageStream {
			suffix = "/message:stream"
		}
		return httpJSONRequest{
			method: http.MethodPost,
			path:   httpJSONPath(p.Tenant, suffix),
			body:   p,
		}, nil
	case protocol.MethodTasksGet:
		p, ok := params.(protocol.TaskQueryParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid GetTask parameters %T", params)
		}
		if len(p.Metadata) != 0 {
			return httpJSONRequest{}, taskmanager.ErrInvalidParams("GetTask metadata cannot be represented by the HTTP+JSON binding")
		}
		query := make(url.Values)
		setOptionalIntQuery(query, "historyLength", p.HistoryLength)
		return httpJSONRequest{
			method: http.MethodGet,
			path:   httpJSONPath(p.Tenant, "/tasks/"+url.PathEscape(p.ID)),
			query:  query,
		}, nil
	case protocol.MethodTasksList:
		p, ok := params.(protocol.ListTasksParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid ListTasks parameters %T", params)
		}
		if len(p.Metadata) != 0 {
			return httpJSONRequest{}, taskmanager.ErrInvalidParams("ListTasks metadata cannot be represented by the HTTP+JSON binding")
		}
		query := make(url.Values)
		setQuery(query, "contextId", p.ContextID)
		if p.Status != "" && p.Status != protocol.TaskStateUnspecified {
			query.Set("status", string(p.Status))
		}
		setOptionalIntQuery(query, "pageSize", p.PageSize)
		setQuery(query, "pageToken", p.PageToken)
		setOptionalIntQuery(query, "historyLength", p.HistoryLength)
		setQuery(query, "statusTimestampAfter", p.StatusTimestampAfter)
		if p.IncludeArtifacts != nil {
			query.Set("includeArtifacts", strconv.FormatBool(*p.IncludeArtifacts))
		}
		return httpJSONRequest{method: http.MethodGet, path: httpJSONPath(p.Tenant, "/tasks"), query: query}, nil
	case protocol.MethodTasksCancel:
		p, ok := params.(protocol.TaskIDParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid CancelTask parameters %T", params)
		}
		return httpJSONRequest{
			method: http.MethodPost,
			path:   httpJSONPath(p.Tenant, "/tasks/"+url.PathEscape(p.ID)+":cancel"),
			body:   p,
		}, nil
	case protocol.MethodTasksResubscribe:
		p, ok := params.(protocol.TaskIDParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid SubscribeToTask parameters %T", params)
		}
		if len(p.Metadata) != 0 {
			return httpJSONRequest{}, taskmanager.ErrInvalidParams("SubscribeToTask metadata cannot be represented by the HTTP+JSON binding")
		}
		return httpJSONRequest{
			method: http.MethodPost,
			path:   httpJSONPath(p.Tenant, "/tasks/"+url.PathEscape(p.ID)+":subscribe"),
		}, nil
	case protocol.MethodTasksPushNotificationConfigSet:
		p, ok := params.(protocol.TaskPushNotificationConfig)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid CreateTaskPushNotificationConfig parameters %T", params)
		}
		return httpJSONRequest{
			method: http.MethodPost,
			path: httpJSONPath(
				p.Tenant,
				"/tasks/"+url.PathEscape(p.TaskID)+"/pushNotificationConfigs",
			),
			body: p,
		}, nil
	case protocol.MethodTasksPushNotificationConfigGet:
		p, ok := params.(protocol.GetTaskPushNotificationConfigParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid GetTaskPushNotificationConfig parameters %T", params)
		}
		return httpJSONRequest{
			method: http.MethodGet,
			path: httpJSONPath(
				p.Tenant,
				"/tasks/"+url.PathEscape(p.TaskID)+"/pushNotificationConfigs/"+url.PathEscape(p.ID),
			),
		}, nil
	case protocol.MethodTasksPushNotificationConfigList:
		p, ok := params.(protocol.ListTaskPushNotificationConfigsParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid ListTaskPushNotificationConfigs parameters %T", params)
		}
		query := make(url.Values)
		setOptionalIntQuery(query, "pageSize", p.PageSize)
		setQuery(query, "pageToken", p.PageToken)
		return httpJSONRequest{
			method: http.MethodGet,
			path: httpJSONPath(
				p.Tenant,
				"/tasks/"+url.PathEscape(p.TaskID)+"/pushNotificationConfigs",
			),
			query: query,
		}, nil
	case protocol.MethodTasksPushNotificationConfigDelete:
		p, ok := params.(protocol.DeleteTaskPushNotificationConfigParams)
		if !ok {
			return httpJSONRequest{}, fmt.Errorf("invalid DeleteTaskPushNotificationConfig parameters %T", params)
		}
		return httpJSONRequest{
			method: http.MethodDelete,
			path: httpJSONPath(
				p.Tenant,
				"/tasks/"+url.PathEscape(p.TaskID)+"/pushNotificationConfigs/"+url.PathEscape(p.ID),
			),
		}, nil
	case protocol.MethodAgentAuthenticatedExtendedCard:
		p, _ := params.(extendedCardParams)
		return httpJSONRequest{method: http.MethodGet, path: httpJSONPath(p.Tenant, "/extendedAgentCard")}, nil
	default:
		return httpJSONRequest{}, fmt.Errorf("HTTP+JSON operation %q is not supported", operation)
	}
}

func httpJSONPath(tenant, suffix string) string {
	if tenant == "" {
		return suffix
	}
	return "/" + url.PathEscape(tenant) + suffix
}

func setQuery(query url.Values, name, value string) {
	if value != "" {
		query.Set(name, value)
	}
}

func setOptionalIntQuery(query url.Values, name string, value *int) {
	if value != nil {
		query.Set(name, strconv.Itoa(*value))
	}
}

func (c *A2AClient) newHTTPJSONRequest(
	ctx context.Context,
	spec httpJSONRequest,
	stream bool,
	opts ...RequestOption,
) (*http.Request, error) {
	var body io.Reader
	if spec.body != nil {
		encoded, err := json.Marshal(spec.body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal HTTP+JSON request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, c.httpJSONURL(spec.path, spec.query), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP+JSON request: %w", err)
	}
	if spec.body != nil {
		req.Header.Set("Content-Type", protocol.MediaTypeA2AJSON)
	}
	if stream {
		req.Header.Set("Accept", protocol.MediaTypeEventStream)
	} else {
		req.Header.Set("Accept", protocol.MediaTypeA2AJSON+", "+protocol.MediaTypeJSON)
	}
	req.Header.Set("A2A-Version", protocol.ProtocolVersionV1)
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	cfg := &requestConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	for key, value := range cfg.headers {
		req.Header.Set(key, value)
	}
	return req, nil
}

func (c *A2AClient) httpJSONURL(rawPath string, query url.Values) string {
	target := *c.baseURL
	basePath := strings.TrimRight(target.EscapedPath(), "/")
	target.RawPath = basePath + rawPath
	target.Path, _ = url.PathUnescape(target.RawPath)
	target.RawQuery = query.Encode()
	target.Fragment = ""
	return target.String()
}

func validateHTTPJSONResponseType(contentType string) error {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != protocol.MediaTypeA2AJSON && mediaType != protocol.MediaTypeJSON) {
		return fmt.Errorf(
			"unexpected HTTP+JSON response Content-Type %q; expected %s or %s",
			contentType,
			protocol.MediaTypeA2AJSON,
			protocol.MediaTypeJSON,
		)
	}
	return nil
}

func decodeHTTPJSONError(statusCode int, body []byte) error {
	var response struct {
		Error struct {
			Code    int              `json:"code"`
			Status  string           `json:"status"`
			Message string           `json:"message"`
			Details []map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Error.Message == "" {
		return fmt.Errorf("HTTP+JSON request failed with status %d: %s", statusCode, body)
	}
	reason := ""
	taskID := ""
	for _, detail := range response.Error.Details {
		if detail["@type"] != "type.googleapis.com/google.rpc.ErrorInfo" {
			continue
		}
		reason, _ = detail["reason"].(string)
		if metadata, ok := detail["metadata"].(map[string]any); ok {
			taskID, _ = metadata["taskId"].(string)
		}
		break
	}
	switch taskmanager.ErrorCode(reason) {
	case taskmanager.ErrCodeTaskNotFound:
		return taskmanager.ErrTaskNotFound(taskID)
	case taskmanager.ErrCodeTaskNotCancelable:
		return taskmanager.ErrTaskNotCancelable(taskID, protocol.TaskStateUnspecified)
	case taskmanager.ErrCodePushNotificationNotSupported:
		return taskmanager.ErrPushNotificationNotSupported()
	case taskmanager.ErrCodeUnsupportedOperation:
		return taskmanager.ErrUnsupportedOperation(response.Error.Message)
	case taskmanager.ErrCodeContentTypeNotSupported:
		return taskmanager.ErrContentTypeNotSupported(response.Error.Message)
	case taskmanager.ErrCodeInvalidAgentResponse:
		return taskmanager.ErrInvalidAgentResponse(response.Error.Message)
	case taskmanager.ErrCodeAuthenticatedExtendedCardNotConfigured:
		return taskmanager.ErrAuthenticatedExtendedCardNotConfigured()
	case taskmanager.ErrCodeExtensionSupportRequired:
		return taskmanager.ErrExtensionSupportRequired(response.Error.Message)
	case taskmanager.ErrCodeVersionNotSupported:
		return taskmanager.ErrVersionNotSupported(response.Error.Message)
	}
	if statusCode >= 500 {
		return taskmanager.ErrInternalError(response.Error.Message)
	}
	return taskmanager.ErrInvalidParams(response.Error.Message)
}
