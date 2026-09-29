package english_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/english"
)

func TestAndJoinsAListTheWayASentenceDoes(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"":                nil,
		"go":              {"go"},
		"go and rust":     {"go", "rust"},
		"go, js and rust": {"go", "js", "rust"},
		"a, b, c and d":   {"a", "b", "c", "d"},
	}
	for want, items := range cases {
		if got := english.And(items); got != want {
			t.Errorf("And(%q) = %q, want %q", items, got, want)
		}
	}
}

func TestOrOffersAChoiceTheWayASentenceDoes(t *testing.T) {
	t.Parallel()

	if got, want := english.Or([]string{"ocel.json", "ocel.yaml", "ocel.config.ts"}), "ocel.json, ocel.yaml or ocel.config.ts"; got != want {
		t.Errorf("Or = %q, want %q", got, want)
	}
}

func TestQuotedQuotesEachValue(t *testing.T) {
	t.Parallel()

	if got, want := english.And(english.Quoted([]string{"node", "go"})), `"node" and "go"`; got != want {
		t.Errorf("And(Quoted) = %q, want %q", got, want)
	}
}
