package providerserver

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const spanExportInterval = 250 * time.Millisecond

func ExportSpans(ctx context.Context, service string) (context.Context, func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	handed := tracing.Collector{URL: os.Getenv(tracing.CollectorURLEnvVar), Token: os.Getenv(tracing.CollectorTokenEnvVar)}
	ctx = continueHandedTrace(ctx)
	for _, name := range []string{tracing.CollectorURLEnvVar, tracing.CollectorTokenEnvVar, tracing.TraceParentEnvVar} {
		_ = os.Unsetenv(name)
	}
	if handed.URL == "" {
		return ctx, func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(handed.URL),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": localrpc.FormatAuthHeader(handed.Token)}),
	)
	if err != nil {
		return nil, nil, err
	}
	tracing.SetCollector(handed)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(spanExportInterval)),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(provider)
	return ctx, provider.Shutdown, nil
}

func continueHandedTrace(ctx context.Context) context.Context {
	traceparent := os.Getenv(tracing.TraceParentEnvVar)
	if traceparent == "" || trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	return tracing.ContextWithTraceParent(ctx, traceparent)
}
