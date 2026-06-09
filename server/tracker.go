// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package server

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/telemetry"
	"trpc.group/trpc-go/trpc-a2a-go/telemetry/metrics"
)

// metricsTracker holds per-request telemetry state for the server package.
type metricsTracker struct {
	mu sync.Mutex

	method             string
	instruments        *metrics.Instruments
	isStreaming        bool
	startTime          time.Time
	firstTokenTime     time.Time
	firstTokenRecorded bool
	policy             telemetry.FirstTokenPolicy
	errType            string
}

func newMetricsTracker(
	method string,
	isStreaming bool,
	policy telemetry.FirstTokenPolicy,
	inst *metrics.Instruments,
) *metricsTracker {
	if policy == nil {
		policy = telemetry.DefaultFirstTokenPolicy
	}
	return &metricsTracker{
		method:      method,
		instruments: inst,
		isStreaming: isStreaming,
		startTime:   time.Now(),
		policy:      policy,
	}
}

func (t *metricsTracker) onEvent(event protocol.StreamResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.firstTokenRecorded {
		return
	}
	if t.policy.IsStreamingFirstToken(&event) {
		t.firstTokenTime = time.Now()
		t.firstTokenRecorded = true
	}
}

func (t *metricsTracker) observeNonStreamingResult(result *protocol.SendMessageResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isStreaming || t.firstTokenRecorded {
		return
	}
	if t.policy.IsNonStreamingFirstToken(result) {
		t.firstTokenTime = time.Now()
		t.firstTokenRecorded = true
	}
}

func (t *metricsTracker) setError(errType string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.errType = errType
}

func (t *metricsTracker) record(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()

	duration := time.Since(t.startTime).Seconds()

	attrs := []attribute.KeyValue{
		attribute.String(metrics.AttrMethod, t.method),
		attribute.Bool(metrics.AttrIsStreaming, t.isStreaming),
	}
	if t.errType != "" {
		attrs = append(attrs, attribute.String(metrics.AttrErrorType, t.errType))
	}

	attrSet := metric.WithAttributes(attrs...)

	if t.instruments == nil {
		return
	}
	if t.instruments.RequestCount != nil {
		t.instruments.RequestCount.Add(ctx, 1, attrSet)
	}
	if t.instruments.OperationDuration != nil {
		t.instruments.OperationDuration.Record(ctx, duration, attrSet)
	}
	if t.instruments.TimeToFirstToken != nil && t.firstTokenRecorded {
		ttft := t.firstTokenTime.Sub(t.startTime).Seconds()
		t.instruments.TimeToFirstToken.Record(ctx, ttft, attrSet)
	}
}
