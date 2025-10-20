// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"trpc.group/trpc-go/trpc-a2a-go/auth"
)

// Option is a functional option type for configuring the A2AClient.
type Option func(*A2AClient)

// HTTPReqHandler is a custom HTTP request handler for a2a client.
type HTTPReqHandler interface {
	Handle(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error)
}

// WithHTTPClient sets a custom http.Client for the A2AClient.
func WithHTTPClient(client *http.Client) Option {
	return func(c *A2AClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithChannelSize sets the size of the channel for the EventReader.
// The default value is 1024.
func WithChannelSize(channelSize int) Option {
	return func(c *A2AClient) {
		c.channelSize = channelSize
	}
}

// WithBuffer configures the buffer for the EventReader.
// It sets the initial buffer size and the maximum buffer size.
func WithBuffer(initialBufSize, maxBufSize int) Option {
	return func(c *A2AClient) {
		c.initialBufSize = initialBufSize
		c.maxBufSize = maxBufSize
	}
}

// WithTimeout sets the timeout for the underlying http.Client.
// If a custom client was provided via WithHTTPClient, this modifies its timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *A2AClient) {
		if timeout > 0 && c.httpClient != nil {
			c.httpClient.Timeout = timeout
		}
	}
}

// WithUserAgent sets a custom User-Agent header for requests.
func WithUserAgent(userAgent string) Option {
	return func(c *A2AClient) {
		c.userAgent = userAgent
	}
}

// Authentication options

// WithJWTAuth configures the client to use JWT authentication.
func WithJWTAuth(secret []byte, audience, issuer string, lifetime time.Duration) Option {
	return func(c *A2AClient) {
		provider := auth.NewJWTAuthProvider(secret, audience, issuer, lifetime)
		c.authProvider = provider
		c.httpClient = provider.ConfigureClient(c.httpClient)
	}
}

// WithAPIKeyAuth configures the client to use API key authentication.
func WithAPIKeyAuth(apiKey, headerName string) Option {
	return func(c *A2AClient) {
		provider := auth.NewAPIKeyAuthProvider(make(map[string]string), headerName)
		provider.SetClientAPIKey(apiKey)
		c.authProvider = provider
		c.httpClient = provider.ConfigureClient(c.httpClient)
	}
}

// WithOAuth2ClientCredentials configures the client to use OAuth2 client credentials flow.
func WithOAuth2ClientCredentials(clientID, clientSecret, tokenURL string, scopes []string) Option {
	return func(c *A2AClient) {
		provider := auth.NewOAuth2ClientCredentialsProvider(
			clientID,
			clientSecret,
			tokenURL,
			scopes,
		)
		c.authProvider = provider
		c.httpClient = provider.ConfigureClient(c.httpClient)
	}
}

// WithOAuth2TokenSource configures the client to use a custom OAuth2 token source.
func WithOAuth2TokenSource(config *oauth2.Config, tokenSource oauth2.TokenSource) Option {
	return func(c *A2AClient) {
		provider := auth.NewOAuth2AuthProviderWithConfig(config, "", "")
		provider.SetTokenSource(tokenSource)
		c.authProvider = provider
		c.httpClient = provider.ConfigureClient(c.httpClient)
	}
}

// WithAuthProvider allows using a custom auth provider that implements the ClientProvider interface.
func WithAuthProvider(provider auth.ClientProvider) Option {
	return func(c *A2AClient) {
		c.authProvider = provider
		c.httpClient = provider.ConfigureClient(c.httpClient)
	}
}

// WithHTTPReqHandler sets a custom HTTP request handler for the A2AClient.
func WithHTTPReqHandler(handler HTTPReqHandler) Option {
	return func(c *A2AClient) {
		c.httpReqHandler = handler
	}
}

// RequestOption is a functional option type for configuring individual requests.
type RequestOption func(*requestConfig)

// requestConfig holds per-request configuration.
type requestConfig struct {
	headers map[string]string
}

// WithRequestHeaders sets custom HTTP headers for a single request.
// These headers will be added to the specific request only.
// If a header key already exists (e.g., Content-Type, Accept), it will be overwritten.
//
// Example:
//
//	client.SendMessage(ctx, params, client.WithRequestHeaders(map[string]string{
//	    "X-Request-ID": "12345",
//	    "X-Custom-Header": "value",
//	}))
func WithRequestHeaders(headers map[string]string) RequestOption {
	return func(rc *requestConfig) {
		if rc.headers == nil {
			rc.headers = make(map[string]string)
		}
		for k, v := range headers {
			rc.headers[k] = v
		}
	}
}

// WithRequestHeader sets a single custom HTTP header for a single request.
// This is a convenience function for setting one header at a time.
//
// Example:
//
//	client.SendMessage(ctx, params, client.WithRequestHeader("X-Request-ID", "12345"))
func WithRequestHeader(key, value string) RequestOption {
	return func(rc *requestConfig) {
		if rc.headers == nil {
			rc.headers = make(map[string]string)
		}
		rc.headers[key] = value
	}
}
