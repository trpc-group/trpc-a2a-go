// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package pushauth is the JWT/JWKS trust layer for A2A push notifications. It
// holds the signing identity (Authenticator) and the signed Notifier, and is the
// only package that depends on the JWT/JWKS libraries — so a task manager that
// only needs the push.Sender interface (e.g. the redis manager) never inherits
// those dependencies.
//
// The base push package provides transport (push.HTTPSender) and a generic
// signing hook (push.WithAuthorizationHeader); this package supplies the JWT
// implementation of that hook and publishes the matching JWKS.
package pushauth

import (
	"context"
	"crypto/rsa"
	"fmt"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

// Notifier bundles the whole agent-side push-notification capability behind one
// object: delivery (a push.HTTPSender) plus an optional signing identity (an
// Authenticator). Wire the delivery into the TaskManager and the identity into
// the server:
//
//	notifier, _ := pushauth.NewNotifier(pushauth.WithJWT())
//	tm, _  := memory.NewTaskManager(proc, memory.WithPushNotifications(notifier))
//	srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
//	    server.WithPushNotificationAuthenticator(notifier.Authenticator()))
//	// The server publishes the JWKS for that identity automatically.
//
// Passing notifier.Authenticator() (the same identity it signs with) keeps the
// "signer and JWKS must match" invariant by construction.
type Notifier struct {
	sender *push.HTTPSender
	auth   *Authenticator
}

var _ push.Sender = (*Notifier)(nil)

// notifierConfig collects the options; key material is resolved in NewNotifier
// so option application itself cannot fail.
type notifierConfig struct {
	generateJWT bool
	useKey      bool
	privateKey  *rsa.PrivateKey
	keyID       string
	senderOpts  []push.SenderOption
}

// NotifierOption configures a Notifier.
type NotifierOption func(*notifierConfig)

// WithJWT gives the notifier a signing identity with a freshly generated RSA
// key pair: every delivery is signed with a JWT that receivers verify against
// the JWKS the server publishes. The key lives for this process only — for
// deployments that restart or run replicas behind a load balancer, share one
// key via WithJWTKey instead.
func WithJWT() NotifierOption {
	return func(c *notifierConfig) { c.generateJWT = true }
}

// WithJWTKey gives the notifier a signing identity backed by an existing RSA
// private key, so the identity survives restarts and can be shared across
// replicas (each replica signs with the same key the JWKS advertises). An empty
// keyID gets a generated one.
func WithJWTKey(privateKey *rsa.PrivateKey, keyID string) NotifierOption {
	return func(c *notifierConfig) {
		c.useKey = true
		c.privateKey = privateKey
		c.keyID = keyID
	}
}

// WithSenderOptions forwards options to the notifier's underlying push.HTTPSender
// (e.g. push.WithHTTPClient, push.WithRequestDecorator).
func WithSenderOptions(opts ...push.SenderOption) NotifierOption {
	return func(c *notifierConfig) { c.senderOpts = append(c.senderOpts, opts...) }
}

// NewNotifier creates the agent-side push-notification capability. Without a JWT
// option it delivers unsigned notifications (the config's static credentials
// still apply); with WithJWT or WithJWTKey it signs every delivery and exposes
// the matching JWKS via Authenticator().
func NewNotifier(opts ...NotifierOption) (*Notifier, error) {
	var cfg notifierConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	n := &Notifier{}
	switch {
	case cfg.useKey:
		// The caller asked for a key-backed identity: a nil key is a configuration
		// bug and must fail loudly rather than degrade to unsigned deliveries.
		n.auth = NewAuthenticator()
		if err := n.auth.UseKeyPair(cfg.privateKey, cfg.keyID); err != nil {
			return nil, fmt.Errorf("push notifier: install signing key: %w", err)
		}
	case cfg.generateJWT:
		n.auth = NewAuthenticator()
		if err := n.auth.GenerateKeyPair(); err != nil {
			return nil, fmt.Errorf("push notifier: generate signing key: %w", err)
		}
	}

	senderOpts := cfg.senderOpts
	if n.auth != nil {
		// Wire the JWT signer into the base sender through the generic
		// authorization-header hook — this is the only coupling point, and it
		// keeps the jwt/jwx dependency out of the base push package.
		auth := n.auth
		signHook := func(_ context.Context, payload []byte) (string, error) {
			return auth.CreateAuthorizationHeader(payload)
		}
		senderOpts = append([]push.SenderOption{push.WithAuthorizationHeader(signHook)}, senderOpts...)
	}
	n.sender = push.NewHTTPSender(senderOpts...)
	return n, nil
}

// SendPush delivers event to the webhook described by cfg (see push.Sender).
func (n *Notifier) SendPush(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	return n.sender.SendPush(ctx, cfg, event)
}

// Authenticator returns the notifier's signing identity, or nil when the
// notifier was built without a JWT option. The server uses it to publish the
// JWKS; a receiver-side process can use it to verify notifications.
func (n *Notifier) Authenticator() *Authenticator {
	return n.auth
}
