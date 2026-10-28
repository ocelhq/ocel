package runtrace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestASpanStartedAnywhereUnderTheRunsContextLandsInItsTraceUnderTheRoot(t *testing.T) {
	r := startRun(t)
	_, span := otel.Tracer("any/package").Start(r.ContextWithRootSpan(context.Background()), "GET example.com")
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	root := spanNamed(t, spans, "ocel deploy")
	call := spanNamed(t, spans, "GET example.com")
	if call.ParentSpanId != root.SpanId {
		t.Errorf("call parent = %q, want the root %q", call.ParentSpanId, root.SpanId)
	}
	if call.TraceId != r.ID() {
		t.Errorf("call trace = %q, want the run's %q", call.TraceId, r.ID())
	}
}

func TestTwoOpenRunsKeepTheirSpansInTheirOwnTraceFiles(t *testing.T) {
	first, second := startRun(t), startRun(t)
	_, a := otel.Tracer("any/package").Start(first.ContextWithRootSpan(context.Background()), "first call")
	a.End()
	_, b := otel.Tracer("any/package").Start(second.ContextWithRootSpan(context.Background()), "second call")
	b.End()
	for _, r := range []*Trace{first, second} {
		if err := r.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}

	for r, want := range map[*Trace]string{first: "first call", second: "second call"} {
		spans := readTraceDoc(t, r)
		if len(spans) != 2 {
			t.Errorf("trace %s holds %d spans, want its root and %q", r.ID(), len(spans), want)
		}
		spanNamed(t, spans, want)
	}
}

func TestASpanStartedWithNoRunInItsContextIsDropped(t *testing.T) {
	r := startRun(t)
	_, span := otel.Tracer("any/package").Start(context.Background(), "orphan")
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	for _, s := range readTraceDoc(t, r) {
		if s.Name == "orphan" {
			t.Errorf("trace holds %q, a span outside any run", s.Name)
		}
	}
}

func TestARunAndATestRecordingInOneProcessEachKeepTheirSpansWhicheverBeganFirst(t *testing.T) {
	for _, recordingFirst := range []bool{true, false} {
		var r *Trace
		var recording context.Context
		var recorded *tracingtest.Spans
		if recordingFirst {
			recording, recorded = tracingtest.Record(t)
			r = startRun(t)
		} else {
			r = startRun(t)
			recording, recorded = tracingtest.Record(t)
		}
		_, inRun := otel.Tracer("any/package").Start(r.ContextWithRootSpan(context.Background()), "run call")
		inRun.End()
		_, inRecording := otel.Tracer("any/package").Start(recording, "recorded call")
		inRecording.End()
		if err := r.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		spanNamed(t, readTraceDoc(t, r), "run call")
		if got := recorded.Only(t).Name(); got != "recorded call" {
			t.Errorf("recording holds %q, want the span started under it", got)
		}
	}
}
