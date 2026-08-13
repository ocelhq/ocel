package obs

import (
	"context"
	"testing"
)

func TestNoNetworkExporterWithoutAnExplicitEndpoint(t *testing.T) {
	for _, v := range otlpEndpointEnvVars {
		t.Setenv(v, "")
	}

	exp, err := newNetworkExporter(context.Background())
	if err != nil {
		t.Fatalf("newNetworkExporter() = %v", err)
	}
	if exp != nil {
		t.Error("newNetworkExporter() returned an exporter with no OTLP endpoint configured")
	}
}

func TestNetworkExporterOnlyWhenEndpointConfigured(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")

	exp, err := newNetworkExporter(context.Background())
	if err != nil {
		t.Fatalf("newNetworkExporter() = %v", err)
	}
	if exp == nil {
		t.Fatal("newNetworkExporter() returned nil with OTEL_EXPORTER_OTLP_ENDPOINT set")
	}
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() = %v", err)
	}
}
