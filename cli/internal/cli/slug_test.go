package cli

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"My Cool App", "my-cool-app"},
		{"  leading/trailing -- spaces  ", "leading-trailing-spaces"},
		{"Already-slugged-123", "already-slugged-123"},
		{"!!!", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", 63)},
	}
	for _, tc := range cases {
		got := slugify(tc.name)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.name, got, tc.want)
		}
		if got != "" && !validSlug(got) {
			t.Errorf("slugify(%q) = %q, which is not a valid slug", tc.name, got)
		}
	}
}

func TestValidSlug(t *testing.T) {
	valid := []string{"a", "my-app", "app123", "a" + strings.Repeat("b", 62)}
	invalid := []string{"", "-app", "app-", "My-App", "my_app", "my.app", strings.Repeat("a", 64)}

	for _, s := range valid {
		if !validSlug(s) {
			t.Errorf("validSlug(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if validSlug(s) {
			t.Errorf("validSlug(%q) = true, want false", s)
		}
	}
}
