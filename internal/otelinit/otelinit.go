// Package otelinit bootstraps OpenTelemetry tracing for the Lambda functions,
// sending OTLP/HTTP to Honeycomb.
//
// Auth: x-honeycomb-team header set from HONEYCOMB_API_KEY env. If the env is
// empty, Setup returns no-op flush/shutdown — useful for local runs.
package otelinit

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Handle exposes lifecycle hooks for the configured tracer provider.
type Handle struct {
	// Flush forces in-flight spans to export. Call from the Lambda handler
	// wrapper before the runtime freezes the process.
	Flush func(context.Context) error
	// Shutdown closes the exporter. Call on process exit.
	Shutdown func(context.Context) error
}

// noop returns a Handle whose hooks do nothing.
func noop() Handle {
	return Handle{
		Flush:    func(context.Context) error { return nil },
		Shutdown: func(context.Context) error { return nil },
	}
}

// Setup configures the global tracer provider. serviceName is required.
//
// Defaults:
//   - endpoint: api.honeycomb.io (override via OTEL_EXPORTER_OTLP_ENDPOINT)
//   - protocol: OTLP/HTTP protobuf
//   - sampling: parent-based always-on (tiny volume in this project)
//
// If HONEYCOMB_API_KEY is unset, no exporter is configured and a no-op Handle
// is returned so callers can call otel.* unchanged.
func Setup(ctx context.Context, serviceName string) (Handle, error) {
	apiKey := os.Getenv("HONEYCOMB_API_KEY")
	if apiKey == "" {
		return noop(), nil
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "api.honeycomb.io"
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithHeaders(map[string]string{
			"x-honeycomb-team": apiKey,
		}),
		otlptracehttp.WithTimeout(5*time.Second),
	)
	if err != nil {
		return Handle{}, fmt.Errorf("otlp exporter: %w", err)
	}

	// Use resource.New with detectors + our service name. resource.New picks
	// the SDK's bundled schema URL so we don't have to chase semconv versions.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
		),
	)
	if err != nil {
		return Handle{}, fmt.Errorf("resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxExportBatchSize(100),
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return Handle{
		Flush:    tp.ForceFlush,
		Shutdown: tp.Shutdown,
	}, nil
}
