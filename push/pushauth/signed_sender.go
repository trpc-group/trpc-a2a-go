// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package pushauth is the JWT/JWKS trust layer for A2A push notifications. It
// holds the signing identity (JWTSigner) and SignedSender, and is the
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
	"net/http"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

// SignedSender bundles agent-side push-notification delivery with a JWT signing
// identity. Wire the delivery into the TaskManager and its JWKS handler into the
// server:
//
//	sender, _ := pushauth.NewSignedSender()
//	tm, _ := memory.NewTaskManager(proc,
//	    memory.WithPushNotifications(push.Config{Sender: sender}))
//	srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
//	    server.WithPushNotificationJWKSHandler(sender.JWKSHandler()))
//	// The server publishes the identity at its JWKS endpoint.
//
// Passing sender.JWKSHandler() (from the same identity it signs with) keeps the
// "signer and JWKS must match" invariant by construction.
type SignedSender struct {
	sender *push.HTTPSender
	signer *JWTSigner
}

var _ push.Sender = (*SignedSender)(nil)

// signedSenderConfig collects the options; key material is resolved in NewSignedSender
// so option application itself cannot fail.
type signedSenderConfig struct {
	useKey     bool
	privateKey *rsa.PrivateKey
	keyID      string
	senderOpts []push.SenderOption
}

// SignedSenderOption configures a SignedSender.
type SignedSenderOption func(*signedSenderConfig)

// WithJWTKey gives the sender a signing identity backed by an existing RSA
// private key, so the identity survives restarts and can be shared across
// replicas (each replica signs with the same key the JWKS advertises). When
// keyID is empty, a stable ID is derived from the public key.
func WithJWTKey(privateKey *rsa.PrivateKey, keyID string) SignedSenderOption {
	return func(c *signedSenderConfig) {
		c.useKey = true
		c.privateKey = privateKey
		c.keyID = keyID
	}
}

// WithSenderOptions forwards options to the SignedSender's underlying push.HTTPSender
// (e.g. push.WithHTTPClient, push.WithRequestDecorator).
func WithSenderOptions(opts ...push.SenderOption) SignedSenderOption {
	return func(c *signedSenderConfig) { c.senderOpts = append(c.senderOpts, opts...) }
}

// NewSignedSender creates an agent-side push-notification sender with a signing
// identity. By default it generates a process-local RSA key; use WithJWTKey for
// identities that must survive restarts or be shared by replicas. For unsigned
// delivery use push.NewHTTPSender instead.
//
// The client's cfg.Authentication is authoritative, as required by the A2A
// protocol. Static credentials are sent unchanged; a declared scheme without
// credentials is rejected because SignedSender cannot resolve another scheme.
// The sender signs with JWT only when the client omitted Authentication.
func NewSignedSender(opts ...SignedSenderOption) (*SignedSender, error) {
	var cfg signedSenderConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	s := &SignedSender{signer: NewJWTSigner()}
	if cfg.useKey {
		// The caller asked for a key-backed identity: a nil key is a configuration
		// bug and must fail loudly rather than silently use a different identity.
		if err := s.signer.UseKeyPair(cfg.privateKey, cfg.keyID); err != nil {
			return nil, fmt.Errorf("push signed sender: install signing key: %w", err)
		}
	} else {
		if err := s.signer.GenerateKeyPair(); err != nil {
			return nil, fmt.Errorf("push signed sender: generate signing key: %w", err)
		}
	}

	senderOpts := cfg.senderOpts
	// Apply the signing hook after forwarded sender options so callers cannot
	// accidentally replace it with push.WithAuthorizationHeader. This is the
	// only coupling point and keeps JWT/JWK dependencies out of package push.
	signer := s.signer
	signHook := func(
		_ context.Context, config protocol.TaskPushNotificationConfig, payload []byte,
	) (string, error) {
		if config.Authentication != nil {
			return "", fmt.Errorf("client declared authentication scheme %q without static credentials",
				config.Authentication.Scheme)
		}
		return signer.CreateAuthorizationHeader(payload)
	}
	senderOpts = append(senderOpts, push.WithAuthorizationHeader(signHook))
	s.sender = push.NewHTTPSender(senderOpts...)
	return s, nil
}

// SendPush delivers event to the webhook described by cfg (see push.Sender).
func (s *SignedSender) SendPush(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	return s.sender.SendPush(ctx, cfg, event)
}

// JWKSHandler returns an HTTP handler that publishes the public key matching
// this sender's signing identity.
func (s *SignedSender) JWKSHandler() http.Handler {
	return http.HandlerFunc(s.signer.HandleJWKS)
}
