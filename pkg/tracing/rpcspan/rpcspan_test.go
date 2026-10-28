package rpcspan_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/rpcspan"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestAnRPCSpanNamesTheServiceAndMethodItsProcedureCalls(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	_, span := rpcspan.Start(ctx, trace.SpanKindClient, "/ocel.provider.contract.v1.ProviderService/Deploy")
	rpcspan.End(span, nil)

	got := recorded.Only(t)
	if got.Name() != "ocel.provider.contract.v1.ProviderService/Deploy" || got.SpanKind() != trace.SpanKindClient || got.InstrumentationScope().Name != tracing.Scope {
		t.Errorf("span = %q of kind %v under %q, want service/method as a client span under %q", got.Name(), got.SpanKind(), got.InstrumentationScope().Name, tracing.Scope)
	}
	attrs := tracingtest.Attributes(got)
	for key, want := range map[string]string{
		"rpc.system":  "connect_rpc",
		"rpc.service": "ocel.provider.contract.v1.ProviderService",
		"rpc.method":  "Deploy",
	} {
		if attrs[key].AsString() != want {
			t.Errorf("%s = %q, want %q", key, attrs[key].AsString(), want)
		}
	}
}

func TestAFailedRPCSpanRecordsItsErrorAndConnectCode(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	_, span := rpcspan.Start(ctx, trace.SpanKindServer, "/ocel.provider.contract.v1.ProviderService/Configure")
	rpcspan.End(span, connect.NewError(connect.CodeFailedPrecondition, errors.New("already configured")))

	got := recorded.Only(t)
	if got.Status().Code != codes.Error || len(got.Events()) != 1 || got.Events()[0].Name != "exception" {
		t.Errorf("status = %v and events %v, want the error recorded and the span failed", got.Status(), got.Events())
	}
	if code := tracingtest.Attributes(got)["rpc.connect_rpc.error_code"].AsString(); code != "failed_precondition" {
		t.Errorf("rpc.connect_rpc.error_code = %q, want failed_precondition", code)
	}
}
