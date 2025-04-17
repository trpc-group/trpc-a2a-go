// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWithHTTPClient(t *testing.T) {
	// Create a client with the default HTTP client
	client := &A2AClient{
		httpClient: http.DefaultClient,
	}

	// Create a custom HTTP client
	customClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Apply the option
	WithHTTPClient(customClient)(client)

	// Verify the HTTP client was changed
	assert.Equal(t, customClient, client.httpClient)

	// Test with nil client (should not change the current client)
	originalClient := client.httpClient
	WithHTTPClient(nil)(client)
	assert.Equal(t, originalClient, client.httpClient, "Nil client should not change the existing client")
}

func TestWithTimeout(t *testing.T) {
	// Create a client with an HTTP client
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}
	client := &A2AClient{
		httpClient: httpClient,
	}

	// Apply the option
	newTimeout := 20 * time.Second
	WithTimeout(newTimeout)(client)

	// Verify the timeout was changed
	assert.Equal(t, newTimeout, client.httpClient.Timeout)

	// Test with zero timeout (should not change the current timeout)
	originalTimeout := client.httpClient.Timeout
	WithTimeout(0)(client)
	assert.Equal(t, originalTimeout, client.httpClient.Timeout, "Zero timeout should not change the existing timeout")

	// Test with nil HTTP client (should not panic)
	clientWithNilHTTP := &A2AClient{
		httpClient: nil,
	}
	assert.NotPanics(t, func() {
		WithTimeout(10 * time.Second)(clientWithNilHTTP)
	})
}

func TestWithUserAgent(t *testing.T) {
	// Create a client
	client := &A2AClient{
		userAgent: "default-user-agent",
	}

	// Apply the option
	newUserAgent := "custom-user-agent"
	WithUserAgent(newUserAgent)(client)

	// Verify the user agent was changed
	assert.Equal(t, newUserAgent, client.userAgent)

	// Test with empty string (should still set it)
	WithUserAgent("")(client)
	assert.Equal(t, "", client.userAgent)
}
