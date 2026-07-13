// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

const (
	// defaultSendTimeout bounds a single webhook delivery.
	defaultSendTimeout = 30 * time.Second
	// notificationTokenHeader carries the client-supplied validation token,
	// matching the "X-A2A-Notification-Token" header used by the other A2A SDKs
	// (a2a-python, a2a-js), so a spec-conformant receiver finds cfg.Token.
	//nolint:gosec // G101 false positive: this is an HTTP header name, not a credential.
	notificationTokenHeader = "X-A2A-Notification-Token"
	// bearerScheme is the default Authorization scheme for the static-credential
	// fallback when the config carries a credential but no scheme.
	bearerScheme = "Bearer"
)

// AuthHeaderFunc computes the Authorization header value (scheme + credential)
// for a marshaled notification body. It is the generic signing seam: a signer —
// e.g. the JWT authenticator in push/pushauth — is wired in as one of these,
// which keeps the base push package free of any signing-library dependency.
type AuthHeaderFunc func(ctx context.Context, payload []byte) (string, error)

// HTTPSender delivers push notifications over HTTP. The event is marshaled as the
// A2A StreamResponse body (statusUpdate / artifactUpdate / task / message).
//
// Authorization is set from, in priority order: the static credentials the
// client registered on the config's Authentication field (the client declares
// how the agent must authenticate to its webhook), or — when the config declares
// none — a header computed by the configured AuthHeaderFunc (e.g. a JWT the
// receiver verifies against the agent's JWKS endpoint). The config's Token, when
// present, is sent in a separate validation header.
type HTTPSender struct {
	client     *http.Client
	authHeader AuthHeaderFunc
	urlPolicy  func(*url.URL) error
	decorators []func(*http.Request)
}

var _ Sender = (*HTTPSender)(nil)

// SenderOption configures an HTTPSender.
type SenderOption func(*HTTPSender)

// WithHTTPClient sets the HTTP client used to deliver notifications.
func WithHTTPClient(c *http.Client) SenderOption {
	return func(s *HTTPSender) {
		if c != nil {
			s.client = c
		}
	}
}

// WithAuthorizationHeader makes the sender set each notification's Authorization
// header from fn — the signing seam. For JWT/JWKS signing use
// pushauth.NewNotifier, which wires its authenticator in through this option.
func WithAuthorizationHeader(fn AuthHeaderFunc) SenderOption {
	return func(s *HTTPSender) { s.authHeader = fn }
}

// WithRequestDecorator registers a hook invoked on each outgoing push request
// right before it is sent — e.g. to set custom headers, add tracing, or otherwise
// tweak the request. Decorators run in registration order, AFTER the sender's own
// headers (Content-Type / Authorization / A2A-Notification-Token), so they may
// add to or override them. A nil hook is ignored.
func WithRequestDecorator(fn func(*http.Request)) SenderOption {
	return func(s *HTTPSender) {
		if fn != nil {
			s.decorators = append(s.decorators, fn)
		}
	}
}

// WithURLPolicy registers a hook that vets each webhook URL right before
// delivery — return a non-nil error to reject it. Use it to enforce a network
// policy on client-registered webhooks (e.g. HTTPS-only, or an allowlist that
// blocks loopback / private / link-local addresses to mitigate SSRF). The base
// sender already rejects non-http(s) schemes and does not follow redirects; a
// policy adds host/address restrictions on top. A nil hook is ignored.
func WithURLPolicy(fn func(*url.URL) error) SenderOption {
	return func(s *HTTPSender) { s.urlPolicy = fn }
}

// NewHTTPSender creates an HTTPSender with a default 30s timeout. The default
// client does NOT follow redirects: a webhook that responds with a 3xx is
// treated as a failed delivery, since following a redirect off the registered
// host is an SSRF vector.
func NewHTTPSender(opts ...SenderOption) *HTTPSender {
	s := &HTTPSender{client: &http.Client{
		Timeout:       defaultSendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SendPush marshals event to the A2A StreamResponse body and POSTs it to cfg.URL.
func (s *HTTPSender) SendPush(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	if cfg.URL == "" {
		return fmt.Errorf("push sender: config URL is empty")
	}
	// Validate the client-registered URL at the send boundary (the inline-config
	// path can reach here without any prior check). Reject non-http(s) schemes
	// and hostless URLs, then apply the optional network policy (SSRF guard).
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("push sender: invalid URL %q: %w", cfg.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("push sender: URL scheme must be http or https: %q", cfg.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("push sender: URL has no host: %q", cfg.URL)
	}
	if s.urlPolicy != nil {
		if err := s.urlPolicy(u); err != nil {
			return fmt.Errorf("push sender: webhook URL rejected: %w", err)
		}
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("push sender: marshal event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("push sender: build request: %w", err)
	}
	// The A2A spec (§4.3.3 Push Notification Payload) uses application/a2a+json
	// for the delivered StreamResponse body.
	req.Header.Set("Content-Type", "application/a2a+json")

	// Authorization: honor the credential the client registered on the config —
	// it declares how the agent must authenticate to its own webhook (A2A spec).
	// Only when the config declares none do we fall back to the configured
	// signing hook (e.g. a JWT verified against the agent's JWKS).
	if cfg.Authentication != nil && cfg.Authentication.Credentials != "" {
		scheme := cfg.Authentication.Scheme
		if scheme == "" {
			scheme = bearerScheme
		}
		req.Header.Set("Authorization", scheme+" "+cfg.Authentication.Credentials)
	} else if s.authHeader != nil {
		header, err := s.authHeader(ctx, body)
		if err != nil {
			return fmt.Errorf("push sender: authorization header: %w", err)
		}
		req.Header.Set("Authorization", header)
	}
	// Client-validation token, independent of Authorization, per the A2A push spec.
	if cfg.Token != "" {
		req.Header.Set(notificationTokenHeader, cfg.Token)
	}
	// Caller decorators run last so they can set custom headers or override ours.
	for _, decorate := range s.decorators {
		decorate(req)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("push sender: deliver to %s: %w", cfg.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("push sender: %s returned status %s", cfg.URL, resp.Status)
	}
	return nil
}
