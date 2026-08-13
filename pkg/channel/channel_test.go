package channel

import (
	"context"
	"testing"
)

func TestReadinessLine(t *testing.T) {
	t.Parallel()

	t.Run("formats a unix address into the sentinel", func(t *testing.T) {
		t.Parallel()
		got := FormatReadinessLine("unix:/tmp/ocel-provider-abc123.sock")
		want := "OCEL_READY unix:/tmp/ocel-provider-abc123.sock"
		if got != want {
			t.Fatalf("FormatReadinessLine() = %q, want %q", got, want)
		}
	})

	t.Run("round trips every address form", func(t *testing.T) {
		t.Parallel()
		for _, addr := range []string{
			"unix:/tmp/ocel-provider-abc123.sock",
			"tcp:127.0.0.1:54321",
		} {
			t.Run(addr, func(t *testing.T) {
				t.Parallel()
				line := FormatReadinessLine(addr)
				got, ok := ParseReadinessLine(line)
				if !ok {
					t.Fatalf("ParseReadinessLine(%q) ok = false, want true", line)
				}
				if got != addr {
					t.Fatalf("ParseReadinessLine(%q) = %q, want %q", line, got, addr)
				}
			})
		}
	})

	t.Run("ignores other output", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			line string
		}{
			{"nothing at all", ""},
			{"an unrelated log line", "listening on socket...\n"},
			{"a sentinel with a typo", "OCEL_READY_TYPO unix:/tmp/x.sock"},
			{"the sentinel named midway through a line", "some log line mentioning OCEL_READY midway"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, ok := ParseReadinessLine(tc.line); ok {
					t.Fatalf("ParseReadinessLine(%q) ok = true, want false", tc.line)
				}
			})
		}
	})
}

func TestFormatAddr(t *testing.T) {
	t.Parallel()

	t.Run("a unix socket path", func(t *testing.T) {
		t.Parallel()
		got := FormatUnixAddr("/tmp/ocel-provider-abc123.sock")
		want := "unix:/tmp/ocel-provider-abc123.sock"
		if got != want {
			t.Fatalf("FormatUnixAddr() = %q, want %q", got, want)
		}
	})

	t.Run("a loopback port", func(t *testing.T) {
		t.Parallel()
		got := FormatTCPAddr(54321)
		want := "tcp:127.0.0.1:54321"
		if got != want {
			t.Fatalf("FormatTCPAddr() = %q, want %q", got, want)
		}
	})
}

func TestParseAddr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		addr        string
		wantNetwork string
		wantAddress string
		wantErr     bool
	}{
		{
			name:        "a unix socket path",
			addr:        "unix:/tmp/ocel-provider-abc123.sock",
			wantNetwork: "unix",
			wantAddress: "/tmp/ocel-provider-abc123.sock",
		},
		{
			name:        "a loopback host and port",
			addr:        "tcp:127.0.0.1:54321",
			wantNetwork: "tcp",
			wantAddress: "127.0.0.1:54321",
		},
		{name: "an unknown scheme", addr: "bogus:whatever", wantErr: true},
		{name: "an empty unix path", addr: "unix:", wantErr: true},
		{name: "a tcp address with no port", addr: "tcp:127.0.0.1", wantErr: true},
		{name: "a tcp port that is not a number", addr: "tcp:127.0.0.1:http", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			network, address, err := ParseAddr(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseAddr(%q) error = nil, want error", tc.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddr(%q) error = %v", tc.addr, err)
			}
			if network != tc.wantNetwork || address != tc.wantAddress {
				t.Fatalf("ParseAddr(%q) = (%q, %q), want (%q, %q)", tc.addr, network, address, tc.wantNetwork, tc.wantAddress)
			}
		})
	}
}

func TestAuthHeader(t *testing.T) {
	t.Parallel()

	t.Run("formats a bearer token", func(t *testing.T) {
		t.Parallel()
		got := FormatAuthHeader("sekret-token")
		want := "Bearer sekret-token"
		if got != want {
			t.Fatalf("FormatAuthHeader() = %q, want %q", got, want)
		}
	})

	t.Run("round trips a formatted header", func(t *testing.T) {
		t.Parallel()
		header := FormatAuthHeader("sekret-token")
		got, ok := ParseAuthHeader(header)
		if !ok {
			t.Fatalf("ParseAuthHeader(%q) ok = false, want true", header)
		}
		if got != "sekret-token" {
			t.Fatalf("ParseAuthHeader(%q) = %q, want %q", header, got, "sekret-token")
		}
	})

	t.Run("rejects a header that carries no bearer token", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			value string
		}{
			{"no header at all", ""},
			{"a bare token with no scheme", "sekret-token"},
			{"the scheme with no space", "Bearer"},
			{"the scheme with an empty token", "Bearer "},
			{"another scheme", "Basic sekret-token"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, ok := ParseAuthHeader(tc.value); ok {
					t.Fatalf("ParseAuthHeader(%q) ok = true, want false", tc.value)
				}
			})
		}
	})
}

func TestTraceParentContext(t *testing.T) {
	t.Parallel()

	t.Run("round trips a traceparent", func(t *testing.T) {
		t.Parallel()
		ctx := WithTraceParent(context.Background(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		got, ok := TraceParentFromContext(ctx)
		if !ok {
			t.Fatalf("TraceParentFromContext() ok = false, want true")
		}
		if got != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
			t.Fatalf("TraceParentFromContext() = %q", got)
		}
	})

	t.Run("an empty traceparent leaves the context untouched", func(t *testing.T) {
		t.Parallel()
		ctx := WithTraceParent(context.Background(), "")
		if _, ok := TraceParentFromContext(ctx); ok {
			t.Fatalf("TraceParentFromContext() ok = true, want false")
		}
	})

	t.Run("a context with nothing stored", func(t *testing.T) {
		t.Parallel()
		if _, ok := TraceParentFromContext(context.Background()); ok {
			t.Fatalf("TraceParentFromContext() ok = true, want false")
		}
	})

	t.Run("a malformed traceparent leaves the context untouched", func(t *testing.T) {
		t.Parallel()
		ctx := WithTraceParent(context.Background(), "not-a-traceparent")
		if _, ok := TraceParentFromContext(ctx); ok {
			t.Fatalf("TraceParentFromContext() ok = true, want false")
		}
	})
}

func TestValidTraceParent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"a well-formed traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", true},
		{"empty", "", false},
		{"too few fields", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7", false},
		{"too many fields", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra", false},
		{"uppercase hex", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01", false},
		{"a short trace id", "00-4bf92f3577b34da6a3ce929d0e0e4736aa-00f067aa0ba902b7-01", false},
		{"a short span id", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902-01", false},
		{"an all-zero trace id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", false},
		{"an all-zero parent id", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", false},
		{"the reserved ff version", "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"non-hex characters", "00-4bf92f3577b34da6a3ce929d0e0e473g-00f067aa0ba902b7-01", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidTraceParent(tc.value); got != tc.want {
				t.Errorf("ValidTraceParent(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestVerifyAuthHeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
		token string
		want  bool
	}{
		{name: "the session token", value: FormatAuthHeader("sekret-token"), token: "sekret-token", want: true},
		{name: "another session's token", value: FormatAuthHeader("other-token"), token: "sekret-token"},
		{name: "a prefix of the session token", value: FormatAuthHeader("sekret-toke"), token: "sekret-token"},
		{name: "the session token with something appended", value: FormatAuthHeader("sekret-token1"), token: "sekret-token"},
		{name: "no header at all", value: "", token: "sekret-token"},
		{name: "a bare token with no scheme", value: "sekret-token", token: "sekret-token"},
		{name: "an empty token against an empty header", value: "Bearer ", token: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := VerifyAuthHeader(tc.value, tc.token); got != tc.want {
				t.Errorf("VerifyAuthHeader(%q, %q) = %v, want %v", tc.value, tc.token, got, tc.want)
			}
		})
	}
}
