package cli

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestSlugify(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := slugify(tc.name)
			if got != tc.want {
				t.Errorf("slugify(%q) = %q, want %q", tc.name, got, tc.want)
			}
			if got != "" && !projectconfig.ValidSlug(got) {
				t.Errorf("slugify(%q) = %q, which is not a valid slug", tc.name, got)
			}
		})
	}
}
