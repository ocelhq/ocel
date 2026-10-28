package rpcspan

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func Start(ctx context.Context, kind trace.SpanKind, procedure string) (context.Context, trace.Span) {
	service, method, _ := strings.Cut(strings.TrimPrefix(procedure, "/"), "/")
	name, attrs := tracing.NameRPCSpan("connect_rpc", service, method)
	return otel.Tracer(tracing.Scope).Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

func End(span trace.Span, err error) {
	if err != nil {
		span.SetAttributes(attribute.String("rpc.connect_rpc.error_code", connect.CodeOf(err).String()))
	}
	tracing.EndSpan(span, err)
}
