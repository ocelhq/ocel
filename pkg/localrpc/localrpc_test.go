package localrpc

import (
	"testing"
)

func TestAnAddressNamesItsNetworkAndWhereOnIt(t *testing.T) {
	t.Parallel()

	t.Run("a unix socket path", func(t *testing.T) {
		t.Parallel()
		got := FormatUnixAddress("/tmp/ocel-provider-abc123.sock")
		want := "unix:/tmp/ocel-provider-abc123.sock"
		if got != want {
			t.Fatalf("FormatUnixAddress() = %q, want %q", got, want)
		}
	})

	t.Run("a loopback port", func(t *testing.T) {
		t.Parallel()
		got := FormatTCPAddress(54321)
		want := "tcp:127.0.0.1:54321"
		if got != want {
			t.Fatalf("FormatTCPAddress() = %q, want %q", got, want)
		}
	})
}

func TestParsingAnAddressSplitsItsNetworkFromWhereOnIt(t *testing.T) {
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
			network, address, err := ParseAddress(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseAddress(%q) error = nil, want error", tc.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddress(%q) error = %v", tc.addr, err)
			}
			if network != tc.wantNetwork || address != tc.wantAddress {
				t.Fatalf("ParseAddress(%q) = (%q, %q), want (%q, %q)", tc.addr, network, address, tc.wantNetwork, tc.wantAddress)
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

	t.Run("rejects a header with no bearer token", func(t *testing.T) {
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
