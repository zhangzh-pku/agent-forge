package telemetry

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentforge/agentforge/internal/config"
	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Init configures global OpenTelemetry provider and propagators.
// It returns a shutdown function that should be called on process exit.
func Init(ctx context.Context, cfg *config.TelemetryRuntimeConfig) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		xray.Propagator{},
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if cfg == nil || !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := newExporter(cfg.Exporter)
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", cfg.ServiceName))),
	}
	if exporter != nil {
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

func newExporter(name string) (sdktrace.SpanExporter, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none":
		return nil, nil
	case "stdout":
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("telemetry: unsupported exporter %q", name)
	}
}

// TraceIDFromContext returns the hex trace ID when available.
func TraceIDFromContext(ctx context.Context) string {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}
