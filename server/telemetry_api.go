package server

import (
	"context"

	"go.opentelemetry.io/otel/metric"
)

// SetTelemetryMeterProvider injects an externally managed meter provider for this server.
// Call this before InitTelemetry or Start when integrating with framework-managed telemetry.
func (s *A2AServer) SetTelemetryMeterProvider(provider metric.MeterProvider) {
	s.telemetryMeterProvider = provider
	s.telemetryOwnsProvider = false
	s.telemetryShutdown = nil
	if s.telemetryMetrics != nil &&
		(provider == nil || s.telemetryMetrics.MeterProvider != provider) {
		s.telemetryMetrics = nil
	}
}

// InitTelemetry initializes metrics instruments for this server instance.
// It is safe to call multiple times; repeated calls are no-ops if the provider has not changed.
func (s *A2AServer) InitTelemetry(ctx context.Context) error {
	return s.initTelemetry(ctx)
}
