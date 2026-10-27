package console

import "testing"

func TestBaseURL(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		stored string
		want   string
	}{
		{name: "the env console wins over the stored one, trimmed", env: " https://env.example.com/ ", stored: "https://stored.example.com", want: "https://env.example.com"},
		{name: "the stored console is used when the env names none, trimmed", stored: "https://stored.example.com/", want: "https://stored.example.com"},
		{name: "the hosted console at app.ocelhq.com is used when nothing names another, even with OCEL_DEV set", want: "https://app.ocelhq.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(URLEnvVar, tc.env)
			t.Setenv("OCEL_DEV", "1")
			if got := BaseURL(tc.stored); got != tc.want {
				t.Fatalf("BaseURL(%q) = %q, want %q", tc.stored, got, tc.want)
			}
		})
	}
}

func TestNormalizeBaseURLTrimsItAndFallsBackToTheHostedConsoleWithoutReadingTheEnvironment(t *testing.T) {
	t.Setenv(URLEnvVar, "https://env.example.com")
	cases := map[string]string{
		" https://named.example.com/ ": "https://named.example.com",
		"":                             DefaultBaseURL,
		"  ":                           DefaultBaseURL,
	}
	for raw, want := range cases {
		if got := NormalizeBaseURL(raw); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTheAudienceIsTheConsolesCanonicalOrigin(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://Console.Example.COM/app/": "https://console.example.com",
		"https://console.example.com:443":  "https://console.example.com",
		"http://console.example.com:80/":   "http://console.example.com",
		"http://localhost:8080/console":    "http://localhost:8080",
		"https://console.example.com:80":   "https://console.example.com:80",
	}
	for apiURL, want := range cases {
		if got, err := Origin(apiURL); err != nil || got != want {
			t.Errorf("Origin(%q) = %q, %v, want %q", apiURL, got, err, want)
		}
	}
	for _, malformed := range []string{"console.example.com", "https://", "http://[::1"} {
		if got, err := Origin(malformed); err == nil {
			t.Errorf("Origin(%q) = %q, want an error", malformed, got)
		}
	}
}
