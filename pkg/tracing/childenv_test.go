package tracing

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func tracedContext() context.Context {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
}

func TestAChildIsHandedTheCurrentSpanAsItsParentAndTheCollectorToExportToInTheRunsTraceMode(t *testing.T) {
	t.Cleanup(func() { SetCollector(Collector{}) })
	SetCollector(Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "run-token"})

	for value, mode := range map[string]string{"": "on", "full": "full"} {
		t.Setenv(ModeEnvVar, value)
		got := childEnv(tracedContext(), ReadCollector())
		want := []string{
			"TRACEPARENT=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			"OCEL_TRACE_COLLECTOR_URL=http://127.0.0.1:4318/v1/traces",
			"OCEL_TRACE_COLLECTOR_TOKEN=run-token",
			"OCEL_TRACE=" + mode,
		}
		if !slices.Equal(got, want) {
			t.Errorf("childEnv() under %s=%q = %v, want %v", ModeEnvVar, value, got, want)
		}
	}
}

func TestACommandThatOnlyContinuesTheTraceIsHandedItsParentWithoutTheCollector(t *testing.T) {
	t.Cleanup(func() { SetCollector(Collector{}) })
	SetCollector(Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "run-token"})

	want := []string{"TRACEPARENT=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	if got := ParentEnv(tracedContext()); !slices.Equal(got, want) {
		t.Errorf("ParentEnv() = %v, want %v", got, want)
	}
	if got := ParentEnv(context.Background()); got != nil {
		t.Errorf("ParentEnv() outside any trace = %v, want nothing", got)
	}
}

func TestAChildIsNeverHandedTheGenericOTLPExporterVariables(t *testing.T) {
	t.Cleanup(func() { SetCollector(Collector{}) })
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example.com")
	SetCollector(Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "run-token"})

	for _, keyValue := range childEnv(tracedContext(), ReadCollector()) {
		if strings.HasPrefix(keyValue, "OTEL_") {
			t.Errorf("childEnv() holds %s, want the user's OTEL_ settings left to them", keyValue)
		}
	}
}

func TestAChildOfAProcessWithNoCollectorIsHandedOnlyItsParent(t *testing.T) {
	t.Cleanup(func() { SetCollector(Collector{}) })
	SetCollector(Collector{})
	t.Setenv(CollectorURLEnvVar, "http://127.0.0.1:4318/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:4318/v1/traces")

	want := []string{"TRACEPARENT=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	if got := childEnv(tracedContext(), ReadCollector()); !slices.Equal(got, want) {
		t.Errorf("childEnv() = %v, want %v", got, want)
	}
}

func TestAChildOutsideAnyTraceIsHandedOnlyTheCollectorAndTheTraceMode(t *testing.T) {
	t.Cleanup(func() { SetCollector(Collector{}) })
	t.Setenv(ModeEnvVar, "")
	SetCollector(Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "run-token"})

	want := []string{"OCEL_TRACE_COLLECTOR_URL=http://127.0.0.1:4318/v1/traces", "OCEL_TRACE_COLLECTOR_TOKEN=run-token", "OCEL_TRACE=on"}
	if got := childEnv(context.Background(), ReadCollector()); !slices.Equal(got, want) {
		t.Errorf("childEnv() = %v, want %v", got, want)
	}
}

func TestATraceParentReadFromAContextIsTheOneTheContextWasGiven(t *testing.T) {
	t.Parallel()
	const handed = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	ctx := ContextWithTraceParent(context.Background(), handed)

	if got := trace.SpanContextFromContext(ctx).TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace ID = %s, want the one the trace parent names", got)
	}
	if got := TraceParent(ctx); got != handed {
		t.Errorf("TraceParent() = %q, want %q", got, handed)
	}
	if got := TraceParent(context.Background()); got != "" {
		t.Errorf("TraceParent() outside any span = %q, want none", got)
	}
}
