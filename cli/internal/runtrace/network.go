package runtrace

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var otlpEndpointEnvVars = []string{
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
}

func otlpConfigured() bool {
	for _, v := range otlpEndpointEnvVars {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

func newNetworkExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	if !otlpConfigured() {
		return nil, nil
	}
	return otlptracehttp.New(ctx)
}

type networkExporter struct {
	inner sdktrace.SpanExporter
}

func (e networkExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	named := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		named[i] = networkSpan{s}
	}
	return e.inner.ExportSpans(ctx, named)
}

func (e networkExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

type networkSpan struct {
	sdktrace.ReadOnlySpan
}

func (s networkSpan) Name() string {
	values := map[attribute.Key]string{}
	for _, a := range s.ReadOnlySpan.Attributes() {
		values[a.Key] = a.Value.AsString()
	}
	switch {
	case values[AttrStage] != "":
		return values[AttrStage]
	case values[AttrCommand] != "":
		return values[AttrCommand]
	case values[AttrPhase] != "":
		return values[AttrPhase] + " phase"
	default:
		return "span"
	}
}

func (s networkSpan) Attributes() []attribute.KeyValue {
	return filterAttributes(s.ReadOnlySpan.Attributes())
}

func (s networkSpan) Events() []sdktrace.Event {
	return nil
}

func (s networkSpan) Status() sdktrace.Status {
	return sdktrace.Status{Code: s.ReadOnlySpan.Status().Code}
}
