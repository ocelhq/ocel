package obs

import (
	"context"
	"os"

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
