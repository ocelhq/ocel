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

func TestCountNamesHowManyInTheRightNumber(t *testing.T) {
	t.Parallel()
	cases := map[string]int{"0 apps": 0, "1 app": 1, "2 apps": 2}
	for want, n := range cases {
		if got := english.Count(n, "app"); got != want {
			t.Errorf("Count(%d, app) = %q, want %q", n, got, want)
		}
	}
}

func TestNumberPicksTheWordThatAgreesWithACount(t *testing.T) {
	t.Parallel()
	if got := english.Number(1, "it", "them"); got != "it" {
		t.Errorf("Number(1) = %q, want it", got)
	}
	if got := english.Number(3, "it", "them"); got != "them" {
		t.Errorf("Number(3) = %q, want them", got)
	}
}

func TestBytesNamesASizeInBinaryUnits(t *testing.T) {
	t.Parallel()

	cases := map[uint64]string{
		0:             "0 B",
		1023:          "1023 B",
		1024:          "1.0 KiB",
		1536:          "1.5 KiB",
		512 << 20:     "512.0 MiB",
		2 << 30:       "2.0 GiB",
		5 << 40:       "5.0 TiB",
		(1 << 20) - 1: "1024.0 KiB",
	}
	for n, want := range cases {
		if got := english.Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}
