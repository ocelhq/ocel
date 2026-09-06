package cli

import "testing"

func TestPRNumberFromRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref  string
		want string
	}{
		{"refs/pull/123/merge", "123"},
		{"refs/pull/7/head", "7"},
		{"refs/heads/main", ""},
		{"refs/pull/123", ""},
		{"refs/pull//merge", ""},
		{"refs/pull/12a/merge", ""},
		{"refs/pull/123/merge/extra", ""},
		{"refs/pull/123/base", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			t.Parallel()

			if got := prNumberFromRef(tc.ref); got != tc.want {
				t.Errorf("prNumberFromRef(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestPRNumberFromEnvPrefersOcelVar(t *testing.T) {
	t.Setenv("OCEL_PR_NUMBER", "42")
	t.Setenv("GITHUB_REF", "refs/pull/123/merge")

	if got := prNumberFromEnv(); got != "42" {
		t.Errorf("prNumberFromEnv() = %q, want %q", got, "42")
	}
}

func TestPRNumberFromEnvFallsBackToRef(t *testing.T) {
	t.Setenv("OCEL_PR_NUMBER", "")
	t.Setenv("GITHUB_REF", "refs/pull/123/merge")

	if got := prNumberFromEnv(); got != "123" {
		t.Errorf("prNumberFromEnv() = %q, want %q", got, "123")
	}
}
