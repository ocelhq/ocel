package redactedspan_test

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/redactedspan"
)

func TestASecretValueIsRedactedWhereverASpanSaysItDownToFourBytes(t *testing.T) {
	stub := tracetest.SpanStub{
		Name: "connect with pw5c",
		Attributes: []attribute.KeyValue{
			attribute.String("db.statement", "signed with whsec_9fK2mPq and whsec_9fK2mPq_long"),
			attribute.StringSlice("process.command_args", []string{"psql", "pw5c"}),
		},
		Events: []sdktrace.Event{{Name: "retry", Attributes: []attribute.KeyValue{attribute.String("reason", "pw5c rejected")}}},
		Status: sdktrace.Status{Code: codes.Error, Description: "auth failed for pw5c"},
	}
	secrets := &tracing.SecretValues{}
	secrets.Add("pw5c", "whsec_9fK2mPq", "whsec_9fK2mPq_long")

	got := redactedspan.New(stub.Snapshot(), secrets)

	if want := "connect with " + tracing.Redacted; got.Name() != want {
		t.Errorf("name = %q, want %q", got.Name(), want)
	}
	attrs := map[attribute.Key]string{}
	for _, keyValue := range got.Attributes() {
		attrs[keyValue.Key] = keyValue.Value.String()
	}
	if want := "signed with " + tracing.Redacted + " and " + tracing.Redacted; attrs["db.statement"] != want {
		t.Errorf("db.statement = %q, want %q", attrs["db.statement"], want)
	}
	if want := `["psql","` + tracing.Redacted + `"]`; attrs["process.command_args"] != want {
		t.Errorf("process.command_args = %s, want %s", attrs["process.command_args"], want)
	}
	if want := tracing.Redacted + " rejected"; got.Events()[0].Attributes[0].Value.AsString() != want {
		t.Errorf("event reason = %q, want %q", got.Events()[0].Attributes[0].Value.AsString(), want)
	}
	if want := "auth failed for " + tracing.Redacted; got.Status().Description != want {
		t.Errorf("status = %q, want %q", got.Status().Description, want)
	}
	if kept := redactedspan.New(stub.Snapshot(), &tracing.SecretValues{}); kept.Name() != stub.Name {
		t.Errorf("a span redacted without those values is named %q, want %q", kept.Name(), stub.Name)
	}
}

func TestAnAttributeIsRedactedByWhatItsKeySaysItHoldsEvenWithNoSecretValues(t *testing.T) {
	stub := tracetest.SpanStub{
		Name:       "deploy",
		Attributes: []attribute.KeyValue{attribute.String("process.env.DATABASE_URL", "postgres://ocel:hunter2@db/app"), attribute.String("process.env.PORT", "3000")},
	}

	attrs := map[attribute.Key]string{}
	for _, keyValue := range redactedspan.New(stub.Snapshot(), &tracing.SecretValues{}).Attributes() {
		attrs[keyValue.Key] = keyValue.Value.AsString()
	}
	if attrs["process.env.DATABASE_URL"] != tracing.Redacted || attrs["process.env.PORT"] != "3000" {
		t.Errorf("attributes = %v, want DATABASE_URL redacted and PORT kept", attrs)
	}
}
