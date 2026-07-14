// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package push owns the A2A push-notification transport: delivering a task
// update to a client-registered webhook.
//
// A Sender delivers a task update to a single webhook; the TaskManager stores
// the per-task webhook configs internally and, as task events occur, hands each
// one to the configured Sender. Push is opt-in: with no Sender configured the
// manager rejects config registration with PushNotificationNotSupported.
//
// Signing is an optional, dependency-free seam: HTTPSender takes an
// AuthHeaderFunc (WithAuthorizationHeader) to compute the Authorization header.
// The JWT/JWKS trust layer (Authenticator and SignedSender) lives in
// the sub-package push/pushauth, which is the only one that pulls in the JWT
// libraries — so a task manager that needs only the Sender interface stays free
// of them.
package push

import (
	"context"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// Sender delivers a task update to a single push-notification endpoint.
//
// Implementations must be safe for concurrent use. Wrapping a SignedSender
// with custom delivery policy is fine — a field or an embed both work, since the
// server's JWKS identity is configured explicitly (see the server's
// WithPushNotificationAuthenticator) rather than probed off the Sender.
type Sender interface {
	// SendPush delivers event to the webhook described by cfg. It returns nil once
	// the notification is accepted (a 2xx response), or a non-nil error describing
	// the delivery failure.
	//
	// The manager's automatic dispatch calls SendPush asynchronously and
	// best-effort — it logs errors and never fails task execution — whereas a
	// caller pushing manually may treat the returned error as authoritative.
	SendPush(ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse) error
}

// SenderFunc adapts a function to the Sender interface, mirroring
// http.HandlerFunc — useful for inline filtering or delegating delivery to an
// arbitrary function. To keep push enabled while the agent delivers on its own
// schedule, set Config.ManualDelivery instead of supplying a no-op Sender.
type SenderFunc func(ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse) error

// SendPush calls f.
func (f SenderFunc) SendPush(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
) error {
	return f(ctx, cfg, event)
}

// Config configures a task manager's push-notification delivery. It is handed to
// the manager once (e.g. memory.WithPushNotificationsConfig) and lives in this
// package so every task manager — including third-party ones — shares a single
// vocabulary for enabling push.
type Config struct {
	// Sender delivers task updates to registered webhooks as events occur. When
	// nil, push is not supported: the manager rejects config registration with
	// PushNotificationNotSupported (-32003).
	Sender Sender

	// ManualDelivery disables the framework's automatic dispatch while keeping
	// push fully supported — clients register configs and the capability is
	// advertised. The agent delivers by calling the Sender itself, on its own
	// schedule. Requires Sender to be set.
	//
	// Know your webhooks: the processor is handed only the config sent inline
	// with the message (ExecContext.PushConfig). Configs registered afterwards
	// via tasks/pushNotificationConfig/set live in the manager's store — a manual
	// agent that must honor those too needs the TaskManager (e.g. its
	// OnPushNotificationList method) to enumerate them.
	ManualDelivery bool
}
