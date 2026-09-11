package slug

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestFrom(t *testing.T) {
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

			got := From(tc.name)
			if got != tc.want {
				t.Errorf("From(%q) = %q, want %q", tc.name, got, tc.want)
			}
			if got != "" && !projectconfig.ValidSlug(got) {
				t.Errorf("From(%q) = %q, which is not a valid slug", tc.name, got)
			}
		})
	}
}
