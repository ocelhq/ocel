package tracingtest

import (
	"context"
	"slices"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
)

var (
	mu       sync.Mutex
	recorded = map[trace.TraceID][]sdktrace.ReadOnlySpan{}
	provider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor{}))
)

type Spans struct {
	trace trace.TraceID
}

func Record(t testing.TB) (context.Context, *Spans) {
	t.Helper()
	ctx, root := provider.Tracer("tracingtest").Start(context.Background(), t.Name())
	id := root.SpanContext().TraceID()
	mu.Lock()
	recorded[id] = nil
	mu.Unlock()
	t.Cleanup(tracing.RouteTrace(id, provider))
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		delete(recorded, id)
	})
	return ctx, &Spans{trace: id}
}

func (s *Spans) Ended() []sdktrace.ReadOnlySpan {
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(recorded[s.trace])
}

func (s *Spans) Only(t testing.TB) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := s.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended %d spans %q, want 1", len(ended), names(ended))
	}
	return ended[0]
}

func SpanNamed(t testing.TB, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("no span named %q among %q", name, names(spans))
	return nil
}

func ChildrenOf(spans []sdktrace.ReadOnlySpan, parent sdktrace.ReadOnlySpan) []sdktrace.ReadOnlySpan {
	var children []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Parent().SpanID() == parent.SpanContext().SpanID() {
			children = append(children, span)
		}
	}
	return children
}

func Attributes(span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	attrs := map[string]attribute.Value{}
	for _, keyValue := range span.Attributes() {
		attrs[string(keyValue.Key)] = keyValue.Value
	}
	return attrs
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		out = append(out, span.Name())
	}
	return out
}

type processor struct{}

func (processor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (processor) OnEnd(span sdktrace.ReadOnlySpan) {
	mu.Lock()
	defer mu.Unlock()
	id := span.SpanContext().TraceID()
	if spans, recording := recorded[id]; recording {
		recorded[id] = append(spans, span)
	}
}

func (processor) Shutdown(context.Context) error { return nil }

func (processor) ForceFlush(context.Context) error { return nil }
