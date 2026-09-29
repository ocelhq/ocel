package project

import (
	"strings"
	"testing"
)

func TestDeriveSlugTurnsAnyNameIntoAValidSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want string
	}{
		{"My Cool App", "my-cool-app"},
		{"  leading/trailing -- spaces  ", "leading-trailing-spaces"},
		{"Already-slugged-123", "already-slugged-123"},
		{"a--b", "a-b"},
		{"field -- separator", "field-separator"},
		{"!!!", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", 63)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := DeriveSlug(tc.name)
			if got != tc.want {
				t.Errorf("DeriveSlug(%q) = %q, want %q", tc.name, got, tc.want)
			}
			if got != "" && ValidateSlug(got) != nil {
				t.Errorf("DeriveSlug(%q) = %q, which is not a valid slug", tc.name, got)
			}
		})
	}
}

func TestValidateSlugAcceptsOnlyDNSLabelsWithoutTheFieldSeparator(t *testing.T) {
	t.Parallel()

	t.Run("accepts well formed slugs", func(t *testing.T) {
		t.Parallel()

		for _, s := range []string{"a", "acme", "acme-web-1", "1", strings.Repeat("a", 63)} {
			if err := ValidateSlug(s); err != nil {
				t.Errorf("ValidateSlug(%q) = %v, want nil", s, err)
			}
		}
	})

	t.Run("rejects malformed slugs", func(t *testing.T) {
		t.Parallel()

		invalid := []string{
			"",
			"UPPER",
			"Has_Underscore",
			"-leading",
			"trailing-",
			"has space",
			"has.dot",
			"a--b",
			"double--separator--everywhere",
			strings.Repeat("a", 64),
		}
		for _, s := range invalid {
			if ValidateSlug(s) == nil {
				t.Errorf("ValidateSlug(%q) = nil, want an error", s)
			}
		}
	})
}
