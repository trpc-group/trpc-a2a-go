// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package metrics

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

const (
	protocolGRPC = "grpc"
	protocolHTTP = "http"

	defaultServiceName    = "a2a-server"
	defaultServiceVersion = "0.1.0"

	defaultGRPCEndpoint = "localhost:4317"
	defaultHTTPEndpoint = "localhost:4318"
)

// Option configures the NewMeterProvider function.
type Option func(*providerOptions)

type providerOptions struct {
	endpoint           string
	serviceName        string
	serviceVersion     string
	serviceNamespace   string
	protocol           string
	resourceAttributes []attribute.KeyValue
}

// WithEndpoint sets the OTLP exporter endpoint (host:port, no scheme).
// If OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT is set
// and this option is not provided, the environment variable value will be used.
func WithEndpoint(endpoint string) Option {
	return func(o *providerOptions) {
		o.endpoint = endpoint
	}
}

// WithProtocol sets the export protocol. Supported values: "grpc" (default), "http".
func WithProtocol(protocol string) Option {
	return func(o *providerOptions) {
		o.protocol = protocol
	}
}

// WithServiceName sets the service.name resource attribute.
func WithServiceName(name string) Option {
	return func(o *providerOptions) {
		o.serviceName = name
	}
}

// WithServiceVersion sets the service.version resource attribute.
func WithServiceVersion(version string) Option {
	return func(o *providerOptions) {
		o.serviceVersion = version
	}
}

// WithServiceNamespace sets the service.namespace resource attribute.
func WithServiceNamespace(namespace string) Option {
	return func(o *providerOptions) {
		o.serviceNamespace = namespace
	}
}

// WithResourceAttributes appends custom resource attributes to the meter provider.
func WithResourceAttributes(attrs ...attribute.KeyValue) Option {
	return func(o *providerOptions) {
		o.resourceAttributes = append(o.resourceAttributes, attrs...)
	}
}

// NewMeterProvider creates a new OpenTelemetry SDK MeterProvider with OTLP export.
// Supports gRPC and HTTP protocols. Endpoint can be configured via options or
// environment variables OTEL_EXPORTER_OTLP_METRICS_ENDPOINT / OTEL_EXPORTER_OTLP_ENDPOINT.
func NewMeterProvider(ctx context.Context, opts ...Option) (*sdkmetric.MeterProvider, error) {
	o := &providerOptions{
		serviceName:    defaultServiceName,
		serviceVersion: defaultServiceVersion,
		protocol:       protocolGRPC,
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.endpoint == "" {
		o.endpoint = resolveEndpoint(o.protocol)
	}

	res, err := buildResource(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	var mp *sdkmetric.MeterProvider
	switch o.protocol {
	case protocolHTTP:
		mp, err = newHTTPMeterProvider(ctx, res, o.endpoint)
	default:
		mp, err = newGRPCMeterProvider(ctx, res, o.endpoint)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize meter provider: %w", err)
	}

	return mp, nil
}

func resolveEndpoint(protocol string) string {
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); ep != "" {
		return ep
	}
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		return ep
	}
	if protocol == protocolHTTP {
		return defaultHTTPEndpoint
	}
	return defaultGRPCEndpoint
}

func buildResource(ctx context.Context, o *providerOptions) (*resource.Resource, error) {
	resourceOpts := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(o.serviceName),
			semconv.ServiceVersion(o.serviceVersion),
		),
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
	}

	if o.serviceNamespace != "" {
		resourceOpts = append(resourceOpts,
			resource.WithAttributes(semconv.ServiceNamespace(o.serviceNamespace)))
	}
	if len(o.resourceAttributes) > 0 {
		resourceOpts = append(resourceOpts, resource.WithAttributes(o.resourceAttributes...))
	}

	return resource.New(ctx, resourceOpts...)
}

func newHTTPMeterProvider(ctx context.Context, res *resource.Resource, endpoint string) (*sdkmetric.MeterProvider, error) {
	exporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create HTTP metrics exporter: %w", err)
	}

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		sdkmetric.WithResource(res),
	), nil
}

func newGRPCMeterProvider(ctx context.Context, res *resource.Resource, endpoint string) (*sdkmetric.MeterProvider, error) {
	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create gRPC metrics exporter: %w", err)
	}

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		sdkmetric.WithResource(res),
	), nil
}
