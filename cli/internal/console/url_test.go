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
		{name: "the hosted console is used when nothing names another, even with OCEL_DEV set", want: DefaultBaseURL},
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
