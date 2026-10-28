package tracing

import (
	"context"
	"crypto/rand"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

var (
	routesInstalled sync.Once
	routesMu        sync.RWMutex
	routes          = map[trace.TraceID]trace.TracerProvider{}
)

func RouteTrace(id trace.TraceID, provider trace.TracerProvider) (remove func()) {
	routesInstalled.Do(func() { otel.SetTracerProvider(routedProvider{}) })
	routesMu.Lock()
	routes[id] = provider
	routesMu.Unlock()
	return sync.OnceFunc(func() {
		routesMu.Lock()
		defer routesMu.Unlock()
		delete(routes, id)
	})
}

func findRoute(id trace.TraceID) (trace.TracerProvider, bool) {
	routesMu.RLock()
	defer routesMu.RUnlock()
	provider, ok := routes[id]
	return provider, ok
}

type routedProvider struct{ embedded.TracerProvider }

func (routedProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return routedTracer{name: name, opts: opts}
}

type routedTracer struct {
	embedded.Tracer
	name string
	opts []trace.TracerOption
}

func (t routedTracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	provider, ok := findRoute(trace.SpanContextFromContext(ctx).TraceID())
	if !ok {
		return startUnroutedSpan(ctx)
	}
	return provider.Tracer(t.name, t.opts...).Start(ctx, spanName, opts...)
}

func startUnroutedSpan(ctx context.Context) (context.Context, trace.Span) {
	traceID := trace.SpanContextFromContext(ctx).TraceID()
	if !traceID.IsValid() {
		_, _ = rand.Read(traceID[:])
	}
	var spanID trace.SpanID
	_, _ = rand.Read(spanID[:])
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID}))
	return ctx, trace.SpanFromContext(ctx)
}
