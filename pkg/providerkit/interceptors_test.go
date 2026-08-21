package providerkit

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/pkg/channel"
)

func TestExtractTraceParent(t *testing.T) {
	t.Parallel()

	t.Run("stores a traceparent header into the context", func(t *testing.T) {
		t.Parallel()
		header := http.Header{}
		header.Set(channel.TraceParentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		ctx := extractTraceParent(context.Background(), header)
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
		ctx := extractTraceParent(context.Background(), http.Header{})
		if _, ok := channel.TraceParentFromContext(ctx); ok {
			t.Fatalf("TraceParentFromContext() ok = true, want false")
		}
	})

	t.Run("a malformed traceparent header is rejected rather than propagated", func(t *testing.T) {
		t.Parallel()
		header := http.Header{}
		header.Set(channel.TraceParentHeader, "not-a-traceparent")
		ctx := extractTraceParent(context.Background(), header)
		if _, ok := channel.TraceParentFromContext(ctx); ok {
			t.Fatalf("TraceParentFromContext() ok = true, want false")
		}
	})
}

func TestAuthInterceptorChecksTheSessionToken(t *testing.T) {
	t.Parallel()

	auth := &authenticator{token: "a-token"}

	header := http.Header{}
	header.Set("Authorization", channel.FormatAuthHeader("a-token"))
	if err := auth.check(header); err != nil {
		t.Fatalf("check() with the session token: error = %v", err)
	}

	header.Set("Authorization", channel.FormatAuthHeader("another-token"))
	if err := auth.check(header); err == nil {
		t.Fatal("check() accepted a token the CLI never issued")
	}

	if err := auth.check(http.Header{}); err == nil {
		t.Fatal("check() accepted a request with no Authorization header")
	}
}
