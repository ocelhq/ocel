package tracing_test

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestEndingASpanWithAnErrorRecordsItAndMarksTheSpanFailed(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	_, failed := otel.Tracer("test").Start(ctx, "failed")
	tracing.EndSpan(failed, errors.New("no such stack"))
	_, worked := otel.Tracer("test").Start(ctx, "worked")
	tracing.EndSpan(worked, nil)

	ended := spans.Ended()
	got := tracingtest.SpanNamed(t, ended, "failed")
	if got.Status().Code != codes.Error || got.Status().Description != "no such stack" || len(got.Events()) != 1 || got.Events()[0].Name != "exception" {
		t.Errorf("failed span status = %v and events %v, want the error recorded and the span failed", got.Status(), got.Events())
	}
	if ok := tracingtest.SpanNamed(t, ended, "worked"); ok.Status().Code != codes.Unset || len(ok.Events()) != 0 {
		t.Errorf("worked span status = %v and events %v, want neither", ok.Status(), ok.Events())
	}
}
