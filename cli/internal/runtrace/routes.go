package runtrace

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
)

var (
	installed             sync.Once
	openTraces            = &traceRoutes{traces: map[trace.TraceID]*Trace{}}
	processTracerProvider *sdktrace.TracerProvider
)

func tracerProvider() *sdktrace.TracerProvider {
	installed.Do(func() {
		processTracerProvider = newTracerProvider(cliServiceName)
		otel.SetTextMapPropagator(propagation.TraceContext{})
	})
	return processTracerProvider
}

func newTracerProvider(service string) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(
		sdktrace.WithIDGenerator(runIDGenerator{}),
		sdktrace.WithSpanProcessor(openTraces),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String(serviceNameKey, service))),
	)
}

type traceRoutes struct {
	mu     sync.RWMutex
	traces map[trace.TraceID]*Trace
}

func (r *traceRoutes) add(t *Trace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.traces[t.traceID] = t
	if t.isRecording() {
		t.unroute = tracing.RouteTrace(t.traceID, processTracerProvider)
	}
}

func (r *traceRoutes) remove(t *Trace) {
	t.unroute()
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.traces, t.traceID)
}

func (r *traceRoutes) find(id trace.TraceID) (*Trace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.traces[id]
	return t, ok
}

func (r *traceRoutes) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	if t, ok := r.find(s.SpanContext().TraceID()); ok && t.isRecording() {
		t.file.OnStart(ctx, s)
		if t.network != nil {
			t.network.OnStart(ctx, s)
		}
	}
}

func (r *traceRoutes) OnEnd(s sdktrace.ReadOnlySpan) {
	if t, ok := r.find(s.SpanContext().TraceID()); ok && t.isRecording() {
		t.file.OnEnd(s)
		if t.network != nil {
			t.network.OnEnd(s)
		}
	}
}

func (r *traceRoutes) Shutdown(context.Context) error { return nil }

func (r *traceRoutes) ForceFlush(context.Context) error { return nil }
