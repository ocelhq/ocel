package redactedspan

import (
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
)

type Span struct {
	sdktrace.ReadOnlySpan
	secrets *tracing.SecretValues
}

func New(span sdktrace.ReadOnlySpan, secrets *tracing.SecretValues) Span {
	return Span{ReadOnlySpan: span, secrets: secrets}
}

func (s Span) Name() string {
	return s.secrets.RedactText(s.ReadOnlySpan.Name())
}

func (s Span) Attributes() []attribute.KeyValue {
	return s.secrets.RedactAttributes(s.ReadOnlySpan.Attributes())
}

func (s Span) Events() []sdktrace.Event {
	events := s.ReadOnlySpan.Events()
	if len(events) == 0 {
		return nil
	}
	out := make([]sdktrace.Event, len(events))
	for i, event := range events {
		event.Name, event.Attributes = s.secrets.RedactText(event.Name), s.secrets.RedactAttributes(event.Attributes)
		out[i] = event
	}
	return out
}

func (s Span) Status() sdktrace.Status {
	status := s.ReadOnlySpan.Status()
	status.Description = s.secrets.RedactText(status.Description)
	return status
}
