package deploy

import "testing"

func TestOnlyAPullRequestMergeOrHeadRefNamesAPRNumber(t *testing.T) {
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

func TestTheOcelPRNumberWinsOverTheGitHubRef(t *testing.T) {
	t.Setenv(PRNumberEnvVar, "42")
	t.Setenv("GITHUB_REF", "refs/pull/123/merge")

	if got := DiscoverPRNumber(); got != "42" {
		t.Errorf("DiscoverPRNumber() = %q, want %q", got, "42")
	}
}

func TestTheGitHubRefNamesThePRNumberWhenTheOcelOneIsUnset(t *testing.T) {
	t.Setenv(PRNumberEnvVar, "")
	t.Setenv("GITHUB_REF", "refs/pull/123/merge")

	if got := DiscoverPRNumber(); got != "123" {
		t.Errorf("DiscoverPRNumber() = %q, want %q", got, "123")
	}
}
