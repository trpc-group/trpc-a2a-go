// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

type clientBinding interface {
	request(
		ctx context.Context,
		client *A2AClient,
		id string,
		method string,
		params any,
		result any,
		opts ...RequestOption,
	) error
	stream(
		ctx context.Context,
		client *A2AClient,
		id string,
		method string,
		params any,
		opts ...RequestOption,
	) (*http.Response, error)
	decodeSSEData(data []byte) ([]byte, error)
}

type jsonRPCBinding struct{}

type extendedCardParams struct {
	Tenant string `json:"tenant,omitempty"`
}

func (jsonRPCBinding) request(
	ctx context.Context,
	client *A2AClient,
	id string,
	method string,
	params any,
	result any,
	opts ...RequestOption,
) error {
	request := jsonrpc.NewRequest(method, id)
	if extended, ok := params.(extendedCardParams); ok && extended.Tenant == "" {
		params = nil
	}
	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("failed to marshal params: %w", err)
		}
		request.Params = paramsBytes
	}
	response, err := client.doRequest(ctx, request, opts...)
	if err != nil {
		return err
	}
	if response.Error != nil {
		return response.Error
	}
	if result == nil {
		return nil
	}
	if len(response.Result) == 0 {
		return fmt.Errorf("rpc response missing required 'result' field for id %v", request.ID)
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("failed to unmarshal rpc result: %w. Raw result: %s", err, response.Result)
	}
	return nil
}

func (jsonRPCBinding) stream(
	ctx context.Context,
	client *A2AClient,
	id string,
	method string,
	params any,
	opts ...RequestOption,
) (*http.Response, error) {
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}
	return client.sendA2AStreamRequest(ctx, id, method, paramsBytes, opts...)
}

func (jsonRPCBinding) decodeSSEData(data []byte) ([]byte, error) {
	var response jsonrpc.RawResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("failed to decode JSON-RPC SSE response: %w", err)
	}
	// Older servers emitted the StreamResponse directly even on the JSON-RPC
	// binding. Preserve that existing client tolerance while new HTTP+JSON
	// requests use the raw form deliberately.
	if response.JSONRPC == "" {
		return data, nil
	}
	if response.JSONRPC != jsonrpc.Version {
		return nil, fmt.Errorf("invalid JSON-RPC SSE response version %q", response.JSONRPC)
	}
	if response.Error != nil {
		return nil, response.Error
	}
	if len(response.Result) == 0 {
		return nil, fmt.Errorf("JSON-RPC SSE response is missing result")
	}
	return response.Result, nil
}

func clientBindingForName(name string) (string, clientBinding, error) {
	switch {
	case strings.EqualFold(name, protocol.ProtocolBindingJSONRPC):
		return protocol.ProtocolBindingJSONRPC, jsonRPCBinding{}, nil
	case strings.EqualFold(name, protocol.ProtocolBindingHTTPJSON):
		return protocol.ProtocolBindingHTTPJSON, httpJSONBinding{}, nil
	default:
		return "", nil, fmt.Errorf("unsupported A2A protocol binding %q", name)
	}
}

func (c *A2AClient) applySelectedTenant(tenant *string) error {
	if c.tenant == "" {
		return nil
	}
	if *tenant != "" && *tenant != c.tenant {
		return fmt.Errorf("request tenant %q does not match configured client tenant %q", *tenant, c.tenant)
	}
	*tenant = c.tenant
	return nil
}
