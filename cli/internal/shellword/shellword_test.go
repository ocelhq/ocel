package shellword_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/shellword"
)

func TestQuoteLeavesASafeWordAsItIsAndQuotesEveryOtherWordWhole(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"./cmd/api":    "./cmd/api",
		"./cmd/my api": "'./cmd/my api'",
		"it's":         `'it'\''s'`,
		"":             "''",
	}
	for word, want := range cases {
		if got := shellword.Quote(word); got != want {
			t.Errorf("Quote(%q) = %s, want %s", word, got, want)
		}
	}
}
