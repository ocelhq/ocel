package tracecontext

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/pkg/channel"
)

func TestExtract(t *testing.T) {
	t.Parallel()

	t.Run("stores a traceparent header into the context", func(t *testing.T) {
		t.Parallel()
		header := http.Header{}
		header.Set(channel.TraceParentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		ctx := extract(context.Background(), header)
		got, ok := channel.TraceParentFromContext(ctx)
		if !ok {
			t.Fatalf("TraceParentFromContext() ok = false, want true")
		}
		if got != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
			t.Fatalf("TraceParentFromContext() = %q", got)
		}
	})

	t.Run("a request with no traceparent header leaves the context untouched", func(t *testing.T) {
		t.Parallel()
		ctx := extract(context.Background(), http.Header{})
		if _, ok := channel.TraceParentFromContext(ctx); ok {
			t.Fatalf("TraceParentFromContext() ok = true, want false")
		}
	})
}
