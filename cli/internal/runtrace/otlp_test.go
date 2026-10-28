package runtrace

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestAChildsSpanLandsUnderItsOwnServiceWithItsKindEventsStatusAndArraysKept(t *testing.T) {
	r := startRun(t)
	env := childEnvOf(r.ContextWithRootSpan(context.Background()))
	child := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(childExporter(t, env)),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "ocel-provider-aws"))),
	)
	parent := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": childEnvValue(env, "TRACEPARENT")})
	_, span := child.Tracer("aws").Start(parent, "s3.PutObject", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.StringSlice("aws.s3.keys", []string{"a", "b"})))
	span.AddEvent("retry", trace.WithAttributes(attribute.Int("attempt", 2)))
	span.SetStatus(codes.Error, "SlowDown")
	span.End()
	if err := child.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	doc := readTracesData(t, r)
	var found *tracev1.Span
	for _, resource := range doc.GetResourceSpans() {
		for _, scope := range resource.GetScopeSpans() {
			for _, s := range scope.GetSpans() {
				if s.GetName() != "s3.PutObject" {
					continue
				}
				found = s
				if got := serviceName(resource.GetResource().GetAttributes()); got != "ocel-provider-aws" {
					t.Errorf("span's resource service = %q, want the child's", got)
				}
				if scope.GetScope().GetName() != "aws" {
					t.Errorf("span's scope = %q, want the child's", scope.GetScope().GetName())
				}
			}
		}
	}
	if found == nil {
		t.Fatal("no s3.PutObject span in the trace file")
	}
	if found.GetKind() != tracev1.Span_SPAN_KIND_CLIENT {
		t.Errorf("kind = %s, want CLIENT", found.GetKind())
	}
	if found.GetStatus().GetCode() != tracev1.Status_STATUS_CODE_ERROR || found.GetStatus().GetMessage() != "SlowDown" {
		t.Errorf("status = %v, want ERROR with its message", found.GetStatus())
	}
	if len(found.GetEvents()) != 1 || found.GetEvents()[0].GetName() != "retry" {
		t.Errorf("events = %v, want the retry", found.GetEvents())
	}
	for _, keyValue := range found.GetAttributes() {
		switch keyValue.GetKey() {
		case "service.name":
			t.Error("the child's service is a span attribute, want it the span's resource")
		case "aws.s3.keys":
			if len(keyValue.GetValue().GetArrayValue().GetValues()) != 2 {
				t.Errorf("aws.s3.keys = %v, want an array of two", keyValue.GetValue())
			}
		}
	}
}

func TestAnArrayOfOneTypeAChildExportsIsReadAsASliceOfThatType(t *testing.T) {
	array := func(values ...*commonv1.AnyValue) *commonv1.AnyValue {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{Values: values}}}
	}
	text := func(s string) *commonv1.AnyValue {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
	}
	number := func(n int64) *commonv1.AnyValue {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: n}}
	}
	flag := func(b bool) *commonv1.AnyValue {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: b}}
	}
	fraction := func(f float64) *commonv1.AnyValue {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: f}}
	}
	for _, tc := range []struct {
		in   *commonv1.AnyValue
		want attribute.Type
	}{
		{array(text("a"), text("b")), attribute.STRINGSLICE},
		{array(number(1), number(2)), attribute.INT64SLICE},
		{array(flag(true)), attribute.BOOLSLICE},
		{array(fraction(0.5)), attribute.FLOAT64SLICE},
		{array(text("a"), number(1)), attribute.SLICE},
	} {
		if got := valueOf(tc.in).Type(); got != tc.want {
			t.Errorf("valueOf(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func readTracesData(t *testing.T, r *Trace) *tracev1.TracesData {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for key, field := range value {
				if id, isString := field.(string); isString && otlpIDFields[key] {
					decoded, err := hex.DecodeString(id)
					if err != nil {
						t.Fatalf("%s = %q, want hex", key, id)
					}
					value[key] = base64.StdEncoding.EncodeToString(decoded)
					continue
				}
				walk(field)
			}
		case []any:
			for _, item := range value {
				walk(item)
			}
		}
	}
	walk(doc)
	normalized, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	data := &tracev1.TracesData{}
	if err := protojson.Unmarshal(normalized, data); err != nil {
		t.Fatalf("trace file is not OTLP/JSON: %v", err)
	}
	return data
}
