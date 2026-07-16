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
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
)

// AuthHeaderFunc computes the Authorization header value (scheme + credential)
// for cfg and a marshaled notification body. Receiving cfg lets resolvers honor
// client-declared dynamic credentials instead of replacing their auth scheme.
type AuthHeaderFunc func(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, payload []byte,
) (string, error)

// RequestDecoratorFunc customizes an outgoing request after standard headers
// have been applied. It receives the full config and may reject the request.
type RequestDecoratorFunc func(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, req *http.Request,
) error

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
	client                     *http.Client
	authHeader                 AuthHeaderFunc
	urlPolicy                  func(*url.URL) error
	decorators                 []RequestDecoratorFunc
	unsafeAllowPrivateNetworks bool
}

var _ Sender = (*HTTPSender)(nil)

// SenderOption configures an HTTPSender.
type SenderOption func(*HTTPSender)

// WithHTTPClient sets the HTTP client used to deliver notifications. The client
// is copied; the sender keeps the 30-second timeout ceiling and never follows
// redirects. A custom RoundTripper remains responsible for its own dial-time
// DNS policy, while the sender still validates the destination before use.
func WithHTTPClient(c *http.Client) SenderOption {
	return func(s *HTTPSender) {
		if c != nil {
			clone := *c
			s.client = &clone
		}
	}
}

// WithAuthorizationHeader makes the sender resolve each notification's
// Authorization header from fn when the client config omits Authentication or
// declares a scheme whose credentials must be resolved dynamically. It is the
// signing seam; for JWT/JWKS signing use pushauth.NewSignedSender.
func WithAuthorizationHeader(fn AuthHeaderFunc) SenderOption {
	return func(s *HTTPSender) { s.authHeader = fn }
}

// WithRequestDecorator registers a hook invoked on each outgoing push request
// right before it is sent — e.g. to set custom headers, add tracing, or otherwise
// tweak the request. Decorators run in registration order, AFTER the sender's own
// headers (Content-Type / Authorization / A2A-Notification-Token), so they may
// add to or override them. A nil hook is ignored.
func WithRequestDecorator(fn RequestDecoratorFunc) SenderOption {
	return func(s *HTTPSender) {
		if fn != nil {
			s.decorators = append(s.decorators, fn)
		}
	}
}

// WithUnsafeAllowPrivateNetworks disables the default SSRF protection that
// rejects loopback, private, link-local and other non-public destinations.
// Use only when webhook owners are trusted and private callbacks are intended.
func WithUnsafeAllowPrivateNetworks() SenderOption {
	return func(s *HTTPSender) { s.unsafeAllowPrivateNetworks = true }
}

// WithURLPolicy registers a hook that vets each webhook URL right before
// delivery — return a non-nil error to reject it. Use it to enforce a network
// policy on client-registered webhooks (e.g. HTTPS-only or an application
// allowlist). The base sender already rejects non-http(s) schemes, private and
// special-use destinations, and redirects. A nil hook is ignored.
func WithURLPolicy(fn func(*url.URL) error) SenderOption {
	return func(s *HTTPSender) { s.urlPolicy = fn }
}

// NewHTTPSender creates an HTTPSender with a default 30s timeout. By default it
// rejects private and special-use destinations at resolution and dial time and
// does not follow redirects. Use WithUnsafeAllowPrivateNetworks only for
// explicitly trusted private callbacks.
func NewHTTPSender(opts ...SenderOption) *HTTPSender {
	s := &HTTPSender{}
	for _, o := range opts {
		o(s)
	}
	s.client = normalizedHTTPClient(s.client, s.unsafeAllowPrivateNetworks)
	return s
}

// SendPush marshals event to the A2A StreamResponse body and POSTs it to cfg.URL.
func (s *HTTPSender) SendPush(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	if err := ValidateConfig(cfg); err != nil {
		return fmt.Errorf("push sender: invalid config: %w", err)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil { // ValidateConfig already parsed it; keep this defensive.
		return fmt.Errorf("push sender: parse destination: %w", err)
	}
	if err := validateDestination(ctx, u, s.unsafeAllowPrivateNetworks); err != nil {
		return fmt.Errorf("push sender: destination rejected: %w", err)
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

	if err := s.applyAuthorization(ctx, cfg, body, req); err != nil {
		return err
	}
	// Client-validation token, independent of Authorization, per the A2A push spec.
	if cfg.Token != "" {
		req.Header.Set(notificationTokenHeader, cfg.Token)
	}
	// Caller decorators run last so they can set custom headers or override ours.
	for _, decorate := range s.decorators {
		if err := decorate(ctx, cfg, req); err != nil {
			return fmt.Errorf("push sender: decorate request: %w", err)
		}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("push sender: deliver webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("push sender: webhook returned status %s", resp.Status)
	}
	return nil
}

func (s *HTTPSender) applyAuthorization(
	ctx context.Context,
	cfg protocol.TaskPushNotificationConfig,
	payload []byte,
	req *http.Request,
) error {
	// A non-nil Authentication is authoritative even when credentials are
	// resolved dynamically. Never replace a declared scheme with framework JWT.
	if cfg.Authentication != nil && cfg.Authentication.Credentials != "" {
		req.Header.Set("Authorization",
			strings.TrimSpace(cfg.Authentication.Scheme)+" "+cfg.Authentication.Credentials)
		return nil
	}
	if s.authHeader != nil {
		header, err := s.authHeader(ctx, cfg, payload)
		if err != nil {
			return fmt.Errorf("push sender: authorization header: %w", err)
		}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
	}
	if cfg.Authentication != nil && req.Header.Get("Authorization") == "" {
		return fmt.Errorf("push sender: credentials unavailable for authentication scheme %q",
			cfg.Authentication.Scheme)
	}
	return nil
}

func normalizedHTTPClient(client *http.Client, unsafeAllowPrivateNetworks bool) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if client.Timeout <= 0 || client.Timeout > defaultSendTimeout {
		client.Timeout = defaultSendTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// Push callbacks should not inherit an ambient proxy silently; deployments
		// that need one can pass an explicit client and own that egress boundary.
		transport.Proxy = nil
		baseDial := transport.DialContext
		if baseDial == nil {
			baseDial = (&net.Dialer{Timeout: defaultSendTimeout, KeepAlive: 30 * time.Second}).DialContext
		}
		transport.DialContext = protectedDialContext(baseDial, unsafeAllowPrivateNetworks)
		client.Transport = transport
	}
	return client
}

func protectedDialContext(
	dial func(context.Context, string, string) (net.Conn, error),
	unsafeAllowPrivateNetworks bool,
) func(context.Context, string, string) (net.Conn, error) {
	if unsafeAllowPrivateNetworks {
		return dial
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid destination address: %w", err)
		}
		addresses, err := resolvePublicAddresses(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, resolved := range addresses {
			conn, err := dial(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("dial public destination: %w", lastErr)
	}
}

func validateDestination(ctx context.Context, u *url.URL, unsafeAllowPrivateNetworks bool) error {
	if unsafeAllowPrivateNetworks {
		return nil
	}
	_, err := resolvePublicAddresses(ctx, u.Hostname())
	return err
}

func resolvePublicAddresses(ctx context.Context, host string) ([]net.IPAddr, error) {
	normalized := strings.TrimSuffix(strings.ToLower(host), ".")
	if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") {
		return nil, fmt.Errorf("localhost is not allowed")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("destination host has no addresses")
	}
	for _, address := range addresses {
		ip := address.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
			!ip.IsGlobalUnicast() || isSharedAddressSpace(ip) {
			return nil, fmt.Errorf("destination resolves to a non-public address")
		}
	}
	return addresses, nil
}

func isSharedAddressSpace(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && ip[0] == 100 && ip[1]&0xc0 == 0x40
}
