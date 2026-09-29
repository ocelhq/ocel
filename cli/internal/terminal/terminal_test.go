package terminal

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var (
	sgrPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	oscPattern = regexp.MustCompile(`\x1b\][0-9]*;[^\a]*\a`)
)

func stripSGR(s string) string { return sgrPattern.ReplaceAllString(s, "") }

func TestFitToWidthIgnoresColorCodes(t *testing.T) {
	t.Parallel()

	plain := "deploying the application"
	colored := "\x1b[32m" + plain + "\x1b[0m"

	for width := 4; width <= len(plain)+4; width++ {
		want := fitToWidth(plain, width)
		if got := stripSGR(fitToWidth(colored, width)); got != want {
			t.Errorf("fitToWidth(colored, %d) visible text = %q, want %q", width, got, want)
		}
	}
}

func TestFitToWidthKeepsTheTrailingReset(t *testing.T) {
	t.Parallel()

	row := "\x1b[32mhello world\x1b[0m"

	cases := []struct {
		name  string
		width int
	}{
		{name: "a cut landing mid-word still closes the colour", width: 6},
		{name: "a cut landing on a space still closes the colour", width: 8},
		{name: "a row that exactly fits keeps its reset", width: 11},
		{name: "a row shorter than the terminal keeps its reset", width: 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := fitToWidth(row, tc.width); !strings.HasSuffix(got, "\x1b[0m") {
				t.Errorf("fitToWidth(row, %d) = %q, want a trailing reset so colour cannot bleed into the next row", tc.width, got)
			}
		})
	}
}

func TestFitToWidthNeverShowsMoreColumnsThanItIsGiven(t *testing.T) {
	t.Parallel()

	row := "\x1b[32m" + strings.Repeat("a", 40) + "\x1b[0m"

	cases := []struct {
		name    string
		columns int
		want    int
	}{
		{name: "a row with room to spare is left whole", columns: 41, want: 40},
		{name: "a row exactly as wide as its columns is left whole", columns: 40, want: 40},
		{name: "a row one column too wide gives up its last column", columns: 39, want: 39},
		{name: "a single column shows a single column", columns: 1, want: 1},
		{name: "no columns show nothing", columns: 0, want: 0},
		{name: "negative columns show nothing", columns: -5, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ansi.StringWidth(fitToWidth(row, tc.columns)); got != tc.want {
				t.Errorf("fitToWidth(row, %d) display width = %d, want %d", tc.columns, got, tc.want)
			}
		})
	}
}

func TestFitToWidthLeavesShortRowsUntouched(t *testing.T) {
	t.Parallel()

	row := "\x1b[2m  … and 3 more\x1b[0m"
	if got := fitToWidth(row, 80); got != row {
		t.Errorf("fitToWidth(row, 80) = %q, want the row unchanged", got)
	}
}

func TestFitToWidthKeepsEscapeSequencesIntact(t *testing.T) {
	t.Parallel()

	row := "ab\x1b[31mcd\x1b[0m"
	for width := 1; width <= 8; width++ {
		got := fitToWidth(row, width)
		if stripped := stripSGR(got); strings.Contains(stripped, "\x1b") {
			t.Errorf("fitToWidth(row, %d) = %q, want no split escape sequence", width, got)
		}
	}
}

func TestFitToWidthKeepsHyperlinksIntact(t *testing.T) {
	t.Parallel()

	row := "ab\x1b]8;;https://example.com\aclick\x1b]8;;\a"
	for width := 1; width <= 10; width++ {
		got := fitToWidth(row, width)
		if !strings.HasSuffix(got, "\x1b]8;;\a") {
			t.Errorf("fitToWidth(row, %d) = %q, want the hyperlink closed so the link cannot swallow the next row", width, got)
		}
		if stripped := oscPattern.ReplaceAllString(got, ""); strings.Contains(stripped, "\x1b") {
			t.Errorf("fitToWidth(row, %d) = %q, want no split escape sequence", width, got)
		}
	}
}

func TestFitToWidthCountsWideRunes(t *testing.T) {
	t.Parallel()

	row := "\x1b[36m日本語テキスト\x1b[0m"

	cases := []struct {
		name  string
		width int
		want  int
	}{
		{name: "a budget an even number of cells wide fills it exactly", width: 8, want: 8},
		{name: "a budget too odd for the next cluster stops short of overflowing", width: 7, want: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ansi.StringWidth(fitToWidth(row, tc.width)); got != tc.want {
				t.Errorf("fitToWidth(row, %d) display width = %d, want %d", tc.width, got, tc.want)
			}
		})
	}
}

func TestFitToWidthHandlesRowsWithNothingToShow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		row  string
	}{
		{name: "an empty row stays empty", row: ""},
		{name: "a row of nothing but escape sequences shows nothing", row: "\x1b[32m\x1b[0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := fitToWidth(tc.row, 40)
			if w := ansi.StringWidth(got); w != 0 {
				t.Errorf("fitToWidth(%q, 40) display width = %d, want 0", tc.row, w)
			}
			if stripSGR(got) != "" {
				t.Errorf("fitToWidth(%q, 40) = %q, want no visible text", tc.row, got)
			}
		})
	}
}
