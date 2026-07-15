// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package push owns the A2A push-notification transport: delivering a task
// update to a client-registered webhook.
//
// A Sender delivers a task update to a single webhook. In automatic mode the
// TaskManager stores per-task webhook configs and hands each event to the
// configured Sender. In manual mode the application owns delivery. Push is
// opt-in: when neither a Sender nor ManualDelivery is configured, the manager
// rejects config registration with PushNotificationNotSupported.
//
// Signing is an optional, dependency-free seam: HTTPSender takes an
// AuthHeaderFunc (WithAuthorizationHeader) to compute the Authorization header
// from the registered config and payload.
// The JWT/JWKS trust layer (JWTSigner, Verifier, and SignedSender) lives in
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
// Implementations must be safe for concurrent use and promptly return when ctx
// is canceled. Wrapping a SignedSender
// with custom delivery policy is fine — a field or an embed both work, since the
// server's JWKS handler is configured explicitly (see the server's
// WithPushNotificationJWKSHandler) rather than probed off the Sender.
type Sender interface {
	// SendPush delivers event to the webhook described by cfg. It returns nil once
	// the notification is accepted (a 2xx response), or a non-nil error describing
	// the delivery failure.
	//
	// The manager's automatic dispatch logs delivery errors and never fails task
	// execution. It preserves event order for each registered config and applies
	// bounded backpressure when its delivery queue is full. A caller pushing
	// manually may treat the returned error as authoritative.
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
// the manager once (e.g. memory.WithPushNotifications) and lives in this
// package so every task manager — including third-party ones — shares a single
// vocabulary for enabling push. The built-in managers' automatic queue is
// process-local: it provides bounded, ordered delivery while the process is
// running, but not durable redelivery after a crash. Use ManualDelivery with a
// durable outbox when that guarantee is required.
type Config struct {
	// Sender delivers task updates to registered webhooks automatically as events
	// occur. It is required for automatic delivery. In ManualDelivery mode the
	// manager does not use it, so the application may keep its sender or durable
	// outbox outside the manager.
	Sender Sender

	// ManualDelivery disables the framework's automatic dispatch while keeping
	// push fully supported — clients register configs and the capability is
	// advertised. It does not require Sender: the application owns delivery and
	// may use a push.Sender, queue, or durable outbox on its own schedule.
	//
	// Know your webhooks: the processor is handed only the config sent inline
	// with the message (ExecContext.PushConfig). Configs registered afterwards
	// via tasks/pushNotificationConfig/set live in the manager's store — a manual
	// agent that must honor those too needs the TaskManager (e.g. its
	// OnPushNotificationList method) to enumerate them.
	ManualDelivery bool

	// MaxConcurrentDeliveries bounds concurrent automatic webhook calls. Events
	// for the same (taskId, configId) are always delivered serially and in enqueue
	// order. Zero uses the framework default; values above 1024 are capped.
	// Ignored in ManualDelivery mode.
	MaxConcurrentDeliveries int

	// DeliveryQueueSize bounds automatic deliveries waiting to be sent. Once the
	// queue is full, task event processing waits for capacity instead of silently
	// dropping a notification or creating an unbounded goroutine. Zero uses the
	// framework default; values above 1,048,576 are capped. Ignored in
	// ManualDelivery mode.
	DeliveryQueueSize int
}
