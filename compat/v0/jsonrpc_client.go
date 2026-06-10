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
	"net/url"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// legacyEventClose is the event the legacy server may send to signal stream
// closure (implementation-specific, not part of the spec).
const legacyEventClose = "close"

// Client speaks the legacy v0.2.x JSON-RPC wire protocol to a not-yet-upgraded
// A2A server while exposing v1 types to the caller: requests are translated
// v1 -> v0 before sending and responses are translated v0 -> v1 on receipt.
type Client struct {
	endpoint   *url.URL
	httpClient *http.Client
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.httpClient = hc }
}

// NewClient creates a legacy-protocol client for the given JSON-RPC endpoint URL.
func NewClient(endpoint string, opts ...ClientOption) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("compat/v0: invalid endpoint URL: %w", err)
	}
	c := &Client{endpoint: u, httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// SendMessage sends a message via the legacy message/send method.
func (c *Client) SendMessage(
	ctx context.Context, params protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	legacyParams := FromV1SendMessageParams(params)
	var result MessageResult
	if err := c.doRequest(ctx, MethodMessageSend, params.RPCID, legacyParams, &result); err != nil {
		return nil, fmt.Errorf("compat/v0 SendMessage: %w", err)
	}
	return ToV1SendMessageResponse(&result)
}

// StreamMessage sends a message via the legacy message/stream method and
// returns a channel of v1 stream responses. The channel is closed when the
// stream ends.
func (c *Client) StreamMessage(
	ctx context.Context, params protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	return c.doStreamRequest(ctx, MethodMessageStream, params.RPCID, FromV1SendMessageParams(params))
}

// GetTasks retrieves a task via the legacy tasks/get method.
func (c *Client) GetTasks(
	ctx context.Context, params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	legacyParams := TaskQueryParams{
		RPCID:         params.RPCID,
		ID:            params.ID,
		HistoryLength: params.HistoryLength,
		Metadata:      params.Metadata,
	}
	var task Task
	if err := c.doRequest(ctx, MethodTasksGet, params.RPCID, legacyParams, &task); err != nil {
		return nil, fmt.Errorf("compat/v0 GetTasks: %w", err)
	}
	return ToV1Task(&task), nil
}

// CancelTasks cancels a task via the legacy tasks/cancel method.
func (c *Client) CancelTasks(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.Task, error) {
	var task Task
	if err := c.doRequest(ctx, MethodTasksCancel, params.RPCID, fromV1TaskIDParams(params), &task); err != nil {
		return nil, fmt.Errorf("compat/v0 CancelTasks: %w", err)
	}
	return ToV1Task(&task), nil
}

// ResubscribeTask reattaches to a task stream via the legacy tasks/resubscribe method.
func (c *Client) ResubscribeTask(
	ctx context.Context, params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	return c.doStreamRequest(ctx, MethodTasksResubscribe, params.RPCID, fromV1TaskIDParams(params))
}

// SetPushNotification configures push notifications via the legacy
// tasks/pushNotificationConfig/set method.
func (c *Client) SetPushNotification(
	ctx context.Context, config protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	var result TaskPushNotificationConfig
	if err := c.doRequest(ctx, MethodTasksPushNotificationConfigSet,
		config.RPCID, FromV1PushConfig(config), &result); err != nil {
		return nil, fmt.Errorf("compat/v0 SetPushNotification: %w", err)
	}
	v1 := ToV1PushConfig(result)
	return &v1, nil
}

// GetPushNotification retrieves the push notification configuration via the
// legacy tasks/pushNotificationConfig/get method.
func (c *Client) GetPushNotification(
	ctx context.Context, params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	var result TaskPushNotificationConfig
	if err := c.doRequest(ctx, MethodTasksPushNotificationConfigGet,
		params.RPCID, fromV1TaskIDParams(params), &result); err != nil {
		return nil, fmt.Errorf("compat/v0 GetPushNotification: %w", err)
	}
	v1 := ToV1PushConfig(result)
	return &v1, nil
}

func fromV1TaskIDParams(p protocol.TaskIDParams) TaskIDParams {
	return TaskIDParams{RPCID: p.RPCID, ID: p.ID, Metadata: p.Metadata}
}

// doRequest performs a unary JSON-RPC call with legacy params and decodes the
// legacy result into out.
func (c *Client) doRequest(
	ctx context.Context, method, rpcID string, params interface{}, out interface{},
) error {
	raw, err := c.doRawRequest(ctx, method, rpcID, params, "application/json")
	if err != nil {
		return err
	}
	defer func() {
		if err := raw.Body.Close(); err != nil {
			log.Errorf("compat/v0: failed to close response body: %v", err)
		}
	}()

	var resp jsonrpc.RawResponse
	if err := json.NewDecoder(raw.Body).Decode(&resp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if len(resp.Result) == 0 {
		return fmt.Errorf("response missing result field")
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("failed to decode result: %w", err)
	}
	return nil
}

// doStreamRequest performs a streaming JSON-RPC call and converts the legacy
// SSE events to v1 stream responses.
func (c *Client) doStreamRequest(
	ctx context.Context, method, rpcID string, params interface{},
) (<-chan protocol.StreamResponse, error) {
	raw, err := c.doRawRequest(ctx, method, rpcID, params, "text/event-stream")
	if err != nil {
		return nil, fmt.Errorf("compat/v0 %s: %w", method, err)
	}
	contentType := raw.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		_ = raw.Body.Close()
		return nil, fmt.Errorf("compat/v0 %s: unexpected Content-Type %q", method, contentType)
	}

	events := make(chan protocol.StreamResponse, 16)
	go c.processSSEStream(ctx, raw.Body, method, events)
	return events, nil
}

// processSSEStream reads legacy SSE events, translates them to v1 and emits
// them on the channel, mirroring the legacy client behavior (close event,
// JSON-RPC envelope unwrapping, kind-based payloads).
func (c *Client) processSSEStream(
	ctx context.Context, body io.ReadCloser, method string, events chan<- protocol.StreamResponse,
) {
	defer func() {
		if err := body.Close(); err != nil {
			log.Errorf("compat/v0: failed to close stream body: %v", err)
		}
	}()
	defer close(events)

	reader := sse.NewEventReader(body)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		data, eventType, err := reader.ReadEvent()
		if err != nil {
			if err != io.EOF {
				log.Errorf("compat/v0: error reading SSE stream for %s: %v", method, err)
			}
			return
		}
		if len(data) == 0 {
			continue
		}
		if eventType == legacyEventClose {
			return
		}

		// Unwrap the JSON-RPC envelope when present.
		var rpcResp jsonrpc.RawResponse
		if json.Unmarshal(data, &rpcResp) == nil && rpcResp.JSONRPC == jsonrpc.Version {
			if rpcResp.Error != nil {
				log.Errorf("compat/v0: JSON-RPC error in SSE event for %s: %v", method, rpcResp.Error)
				continue
			}
			data = rpcResp.Result
		}

		var legacyEvent StreamingMessageEvent
		if err := json.Unmarshal(data, &legacyEvent); err != nil {
			log.Errorf("compat/v0: failed to decode SSE event for %s: %v", method, err)
			continue
		}
		v1Event, err := ToV1StreamResponse(legacyEvent)
		if err != nil {
			log.Warnf("compat/v0: skipping unconvertible SSE event for %s: %v", method, err)
			continue
		}

		select {
		case events <- v1Event:
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) doRawRequest(
	ctx context.Context, method, rpcID string, params interface{}, accept string,
) (*http.Response, error) {
	if rpcID == "" {
		rpcID = protocol.GenerateRPCID()
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}
	request := jsonrpc.NewRequest(method, rpcID)
	request.Params = paramsBytes
	reqBytes, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", accept)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}
