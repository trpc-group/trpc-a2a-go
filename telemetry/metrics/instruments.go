// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package metrics provides OpenTelemetry metric instrumentation for the A2A server.
package metrics

import (
	"fmt"

	"go.opentelemetry.io/otel/metric"
)

const (
	meterName = "trpc_a2a_go.server"

	metricRequestCount      = "a2a.server.request_cnt"
	metricOperationDuration = "a2a.server.operation.duration"
	metricTimeToFirstToken  = "a2a.server.time_to_first_token"

	// AttrMethod is the attribute key for the A2A method name.
	AttrMethod = "a2a.method"
	// AttrIsStreaming is the attribute key that indicates streaming mode.
	AttrIsStreaming = "a2a.is_stream"
	// AttrErrorType is the attribute key for classifying error types.
	AttrErrorType = "error.type"
)

// Instruments holds a set of A2A metric instruments bound to a specific meter provider.
type Instruments struct {
	MeterProvider     metric.MeterProvider
	RequestCount      metric.Int64Counter
	OperationDuration metric.Float64Histogram
	TimeToFirstToken  metric.Float64Histogram
}

// NewInstruments creates a new set of A2A metric instruments from the provided meter provider.
func NewInstruments(mp metric.MeterProvider) (*Instruments, error) {
	if mp == nil {
		return nil, fmt.Errorf("meter provider must not be nil")
	}

	a2aMeter := mp.Meter(meterName)

	requestCount, err := a2aMeter.Int64Counter(
		metricRequestCount,
		metric.WithDescription("Total number of A2A server requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("create request count metric: %w", err)
	}

	operationDuration, err := a2aMeter.Float64Histogram(
		metricOperationDuration,
		metric.WithDescription("Duration of A2A server operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("create operation duration metric: %w", err)
	}

	timeToFirstToken, err := a2aMeter.Float64Histogram(
		metricTimeToFirstToken,
		metric.WithDescription("Time to first token for A2A server operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("create time to first token metric: %w", err)
	}

	return &Instruments{
		MeterProvider:     mp,
		RequestCount:      requestCount,
		OperationDuration: operationDuration,
		TimeToFirstToken:  timeToFirstToken,
	}, nil
}
