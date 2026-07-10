package providerv1

import "testing"

func TestFormatReadinessLine_Unix(t *testing.T) {
	got := FormatReadinessLine("unix:/tmp/ocel-provider-abc123.sock")
	want := "OCEL_PROVIDER_READY unix:/tmp/ocel-provider-abc123.sock"
	if got != want {
		t.Fatalf("FormatReadinessLine() = %q, want %q", got, want)
	}
}

func TestParseReadinessLine_RoundTrip(t *testing.T) {
	addrs := []string{
		"unix:/tmp/ocel-provider-abc123.sock",
		"tcp:127.0.0.1:54321",
	}
	for _, addr := range addrs {
		line := FormatReadinessLine(addr)
		got, ok := ParseReadinessLine(line)
		if !ok {
			t.Fatalf("ParseReadinessLine(%q) ok = false, want true", line)
		}
		if got != addr {
			t.Fatalf("ParseReadinessLine(%q) = %q, want %q", line, got, addr)
		}
	}
}

func TestParseReadinessLine_IgnoresOtherOutput(t *testing.T) {
	lines := []string{
		"",
		"listening on socket...\n",
		"OCEL_PROVIDER_READY_TYPO unix:/tmp/x.sock",
		"some log line mentioning OCEL_PROVIDER_READY midway",
	}
	for _, line := range lines {
		if _, ok := ParseReadinessLine(line); ok {
			t.Fatalf("ParseReadinessLine(%q) ok = true, want false", line)
		}
	}
}

func TestFormatUnixAddr(t *testing.T) {
	got := FormatUnixAddr("/tmp/ocel-provider-abc123.sock")
	want := "unix:/tmp/ocel-provider-abc123.sock"
	if got != want {
		t.Fatalf("FormatUnixAddr() = %q, want %q", got, want)
	}
}

func TestFormatTCPAddr(t *testing.T) {
	got := FormatTCPAddr(54321)
	want := "tcp:127.0.0.1:54321"
	if got != want {
		t.Fatalf("FormatTCPAddr() = %q, want %q", got, want)
	}
}

func TestParseAddr_Unix(t *testing.T) {
	network, address, err := ParseAddr("unix:/tmp/ocel-provider-abc123.sock")
	if err != nil {
		t.Fatalf("ParseAddr() error = %v", err)
	}
	if network != "unix" || address != "/tmp/ocel-provider-abc123.sock" {
		t.Fatalf("ParseAddr() = (%q, %q), want (\"unix\", \"/tmp/ocel-provider-abc123.sock\")", network, address)
	}
}

func TestParseAddr_TCP(t *testing.T) {
	network, address, err := ParseAddr("tcp:127.0.0.1:54321")
	if err != nil {
		t.Fatalf("ParseAddr() error = %v", err)
	}
	if network != "tcp" || address != "127.0.0.1:54321" {
		t.Fatalf("ParseAddr() = (%q, %q), want (\"tcp\", \"127.0.0.1:54321\")", network, address)
	}
}

func TestParseAddr_UnknownScheme(t *testing.T) {
	if _, _, err := ParseAddr("bogus:whatever"); err == nil {
		t.Fatal("ParseAddr() error = nil, want error for unknown scheme")
	}
}

func TestFormatAuthHeader(t *testing.T) {
	got := FormatAuthHeader("sekret-token")
	want := "Bearer sekret-token"
	if got != want {
		t.Fatalf("FormatAuthHeader() = %q, want %q", got, want)
	}
}

func TestParseAuthHeader_RoundTrip(t *testing.T) {
	header := FormatAuthHeader("sekret-token")
	got, ok := ParseAuthHeader(header)
	if !ok {
		t.Fatalf("ParseAuthHeader(%q) ok = false, want true", header)
	}
	if got != "sekret-token" {
		t.Fatalf("ParseAuthHeader(%q) = %q, want %q", header, got, "sekret-token")
	}
}

func TestParseAuthHeader_Invalid(t *testing.T) {
	values := []string{"", "sekret-token", "Bearer", "Bearer ", "Basic sekret-token"}
	for _, v := range values {
		if _, ok := ParseAuthHeader(v); ok {
			t.Fatalf("ParseAuthHeader(%q) ok = true, want false", v)
		}
	}
}
