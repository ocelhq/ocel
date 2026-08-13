package obs

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// otlpEndpointEnvVars are the standard OTel SDK variables that name a
// collector endpoint. Spans leave the machine only when one of these is set
// — an operator opted in explicitly — never by default.
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
