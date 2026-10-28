package providerserver

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestARequestsTraceparentBecomesTheRemoteParentOfTheCallsContext(t *testing.T) {
	header := http.Header{}
	header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	parent := trace.SpanContextFromContext(extractTraceParent(context.Background(), header))
	if got := parent.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %s, want the header's", got)
	}
	if got := parent.SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("span id = %s, want the header's", got)
	}
	if !parent.IsRemote() || !parent.IsSampled() {
		t.Errorf("parent = %+v, want a remote sampled parent", parent)
	}
}

func TestARequestWithAMissingOrMalformedTraceparentHasNoParent(t *testing.T) {
	for name, value := range map[string]string{"missing": "", "malformed": "not-a-traceparent"} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			if value != "" {
				header.Set("traceparent", value)
			}
			if parent := trace.SpanContextFromContext(extractTraceParent(context.Background(), header)); parent.IsValid() {
				t.Errorf("parent = %+v, want none", parent)
			}
		})
	}
}
