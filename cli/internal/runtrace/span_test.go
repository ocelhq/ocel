package runtrace

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func TestASpanStartedWithNoParentIsASpanUnderTheRunsRootSpan(t *testing.T) {
	r := startRun(t)
	id := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	r.Receive(Call{}, started(id, nil, "fake:bucket create"))
	r.Receive(Call{}, ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	ingested := spanNamed(t, spans, "fake:bucket create")

	var root *otlpTestSpan
	for i := range spans {
		if spans[i].ParentSpanId == "" {
			root = &spans[i]
		}
	}
	if root == nil {
		t.Fatal("no root span (a span with no parent) in the trace file")
	}
	if ingested.ParentSpanId != root.SpanId {
		t.Errorf("ingested span parentSpanId = %q, want the run's root span id %q", ingested.ParentSpanId, root.SpanId)
	}
}

func TestOneStepTwoCallsStreamIsASpanUnderEachCallWithItsOwnID(t *testing.T) {
	r := startRun(t)
	root := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	nested := []byte{9, 9, 9, 9, 9, 9, 9, 9}
	now := time.Now()
	var calls []trace.SpanContext
	for range 2 {
		_, call := otel.Tracer("test").Start(r.ContextWithRootSpan(context.Background()), "provider.v1.ProviderService/Deploy")
		r.Receive(Call{Span: call.SpanContext()}, started(root, nil, "provisioning the infra"))
		r.Receive(Call{Span: call.SpanContext()}, started(nested, root, "provisioning the bucket"))
		r.Receive(Call{Span: call.SpanContext()}, ended(nested, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
		r.Receive(Call{Span: call.SpanContext()}, ended(root, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
		call.End()
		calls = append(calls, call.SpanContext())
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	for _, call := range calls {
		step := tracing.StepSpanID(call.SpanID(), root).String()
		var foundRoot, foundNested bool
		for _, s := range spans {
			switch {
			case s.SpanId == step:
				foundRoot = s.Name == "provisioning the infra" && s.ParentSpanId == call.SpanID().String()
			case s.SpanId == tracing.StepSpanID(call.SpanID(), nested).String():
				foundNested = s.Name == "provisioning the bucket" && s.ParentSpanId == step
			}
		}
		if !foundRoot || !foundNested {
			t.Errorf("call %s: root step under the call = %t, nested step under the root step = %t, want both", call.SpanID(), foundRoot, foundNested)
		}
	}
}

func TestAStepACallInAnotherTraceStreamsIsLeftOutOfThisTrace(t *testing.T) {
	r := startRun(t)
	_, stranger := otel.Tracer("test").Start(context.Background(), "provider.v1.ProviderService/Deploy")
	id := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	r.Receive(Call{Span: stranger.SpanContext()}, started(id, nil, "provisioning elsewhere"))
	r.Receive(Call{Span: stranger.SpanContext()}, ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	stranger.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	for _, s := range readTraceDoc(t, r) {
		if s.Name == "provisioning elsewhere" {
			t.Error("trace holds a step a call in another trace streamed")
		}
	}
}

func TestAPhaseSpanIsNamedForItsPhaseApartFromTheWorkNamedInsideIt(t *testing.T) {
	r := startRun(t)
	phase := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	attempt := []byte{2, 2, 2, 2, 2, 2, 2, 2}
	r.Receive(Call{}, started(phase, nil, ""))
	r.Receive(Call{}, started(attempt, phase, "provision"))
	r.Receive(Call{}, ended(attempt, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	r.Receive(Call{}, ended(phase, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	spanNamed(t, spans, "provision phase")
	var named int
	for _, span := range spans {
		if span.Name == "provision" {
			named++
		}
	}
	if named != 1 {
		t.Errorf("%d spans named %q, want only the work the command named so, not its phase too", named, "provision")
	}
}

func receiveStepRecordedByProvider(t *testing.T, r *Trace, call Call) {
	t.Helper()
	id := []byte{3, 3, 3, 3, 3, 3, 3, 3}
	now := time.Now()
	r.Receive(call, started(id, nil, "create resource"))
	r.Receive(call, ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK,
		&progressv1.SpanAttribute{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RECORDED_BY_PROVIDER, Value: "true"},
		&progressv1.SpanAttribute{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_ACTION, Value: "create"},
	))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestAStepAProviderExportingSpansRecordsItselfIsLoggedButNotIngestedAgain(t *testing.T) {
	r := startRun(t)
	receiveStepRecordedByProvider(t, r, Call{Span: trace.SpanContextFromContext(r.ContextWithRootSpan(context.Background())), ExportsSpans: true})

	for _, s := range readTraceDoc(t, r) {
		if s.Name == "create resource" {
			t.Error("trace holds the progress mirror of a step its provider records as its own span")
		}
	}
	if logged := loggedEvents(t, r); len(logged) != 2 {
		t.Errorf("logged %d events, want the step's start and end kept in the run log", len(logged))
	}
}

func TestAStepAProviderExportingNoSpansSaysItRecordsIsIngested(t *testing.T) {
	r := startRun(t)
	receiveStepRecordedByProvider(t, r, Call{Span: trace.SpanContextFromContext(r.ContextWithRootSpan(context.Background()))})

	spanNamed(t, readTraceDoc(t, r), "create resource")
}
