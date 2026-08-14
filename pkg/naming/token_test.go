package naming

import "testing"

func TestTokenKind(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  Kind
		ok    bool
	}{
		{TokenPostgres, KindDatabase, true},
		{TokenBucket, KindBucket, true},
		{"", "", false},
		{"postgres", "", false},
		{"ocel:redis", "", false},
	} {
		got, ok := TokenKind(tc.token)
		if ok != tc.ok || got != tc.want {
			t.Errorf("TokenKind(%q) = %q, %v, want %q, %v", tc.token, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEnvFragment(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{TokenPostgres, "POSTGRES"},
		{TokenBucket, "BUCKET"},
		{"acme:redis", "ACME:REDIS"},
	} {
		if got := EnvFragment(tc.token); got != tc.want {
			t.Errorf("EnvFragment(%q) = %q, want %q", tc.token, got, tc.want)
		}
	}
}
