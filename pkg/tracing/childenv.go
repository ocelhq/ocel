package tracing

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

const TraceParentEnvVar = "TRACEPARENT"

const traceParentHeader = "traceparent"

func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get(traceParentHeader)
}

func ContextWithTraceParent(ctx context.Context, traceparent string) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{traceParentHeader: traceparent})
}

func ParentEnv(ctx context.Context) []string {
	if traceparent := TraceParent(ctx); traceparent != "" {
		return []string{TraceParentEnvVar + "=" + traceparent}
	}
	return nil
}

func childEnv(ctx context.Context, c Collector) []string {
	env := ParentEnv(ctx)
	if c.URL != "" {
		env = append(env, CollectorURLEnvVar+"="+c.URL, CollectorTokenEnvVar+"="+c.Token, ModeEnvVar+"="+string(ReadMode()))
	}
	return env
}
