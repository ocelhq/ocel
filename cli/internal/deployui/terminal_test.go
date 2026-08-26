package deployui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var (
	sgrPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	oscPattern = regexp.MustCompile(`\x1b\][0-9]*;[^\a]*\a`)
)

func stripSGR(s string) string { return sgrPattern.ReplaceAllString(s, "") }

func TestTruncateToWidthIgnoresColorCodes(t *testing.T) {
	t.Parallel()

	plain := "deploying the application"
	colored := "\x1b[32m" + plain + "\x1b[0m"

	for width := 4; width <= len(plain)+4; width++ {
		want := truncateToWidth(plain, width)
		if got := stripSGR(truncateToWidth(colored, width)); got != want {
			t.Errorf("truncateToWidth(colored, %d) visible text = %q, want %q", width, got, want)
		}
	}
}

func TestTruncateToWidthKeepsTheTrailingReset(t *testing.T) {
	t.Parallel()

	row := "\x1b[32mhello world\x1b[0m"

	cases := []struct {
		name  string
		width int
	}{
		{name: "a cut landing mid-word still closes the colour", width: 6},
		{name: "a cut landing on a space still closes the colour", width: 8},
		{name: "a row that exactly fits keeps its reset", width: 12},
		{name: "a row shorter than the terminal keeps its reset", width: 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateToWidth(row, tc.width); !strings.HasSuffix(got, "\x1b[0m") {
				t.Errorf("truncateToWidth(row, %d) = %q, want a trailing reset so colour cannot bleed into the next row", tc.width, got)
			}
		})
	}
}

func TestTruncateToWidthReservesOneColumn(t *testing.T) {
	t.Parallel()

	row := "\x1b[32m" + strings.Repeat("a", 40) + "\x1b[0m"

	cases := []struct {
		name  string
		width int
		want  int
	}{
		{name: "a row with room to spare is left whole", width: 42, want: 40},
		{name: "a row one column short of the edge is left whole", width: 41, want: 40},
		{name: "a row filling the terminal gives up its last column", width: 40, want: 39},
		{name: "a two-column terminal still shows a column", width: 2, want: 1},
		{name: "a one-column terminal still shows a column", width: 1, want: 1},
		{name: "a zero width clamps to a column", width: 0, want: 1},
		{name: "a negative width clamps to a column", width: -5, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ansi.StringWidth(truncateToWidth(row, tc.width)); got != tc.want {
				t.Errorf("truncateToWidth(row, %d) display width = %d, want %d", tc.width, got, tc.want)
			}
		})
	}
}

func TestTruncateToWidthLeavesShortRowsUntouched(t *testing.T) {
	t.Parallel()

	row := "\x1b[2m  … and 3 more\x1b[0m"
	if got := truncateToWidth(row, 80); got != row {
		t.Errorf("truncateToWidth(row, 80) = %q, want the row unchanged", got)
	}
}

func TestTruncateToWidthKeepsEscapeSequencesIntact(t *testing.T) {
	t.Parallel()

	row := "ab\x1b[31mcd\x1b[0m"
	for width := 1; width <= 8; width++ {
		got := truncateToWidth(row, width)
		if stripped := stripSGR(got); strings.Contains(stripped, "\x1b") {
			t.Errorf("truncateToWidth(row, %d) = %q, want no split escape sequence", width, got)
		}
	}
}

func TestTruncateToWidthKeepsHyperlinksIntact(t *testing.T) {
	t.Parallel()

	row := "ab\x1b]8;;https://example.com\aclick\x1b]8;;\a"
	for width := 1; width <= 10; width++ {
		got := truncateToWidth(row, width)
		if !strings.HasSuffix(got, "\x1b]8;;\a") {
			t.Errorf("truncateToWidth(row, %d) = %q, want the hyperlink closed so the link cannot swallow the next row", width, got)
		}
		if stripped := oscPattern.ReplaceAllString(got, ""); strings.Contains(stripped, "\x1b") {
			t.Errorf("truncateToWidth(row, %d) = %q, want no split escape sequence", width, got)
		}
	}
}

func TestTruncateToWidthCountsWideRunes(t *testing.T) {
	t.Parallel()

	row := "\x1b[36m日本語テキスト\x1b[0m"

	cases := []struct {
		name  string
		width int
		want  int
	}{
		{name: "a budget an even number of cells wide fills it exactly", width: 9, want: 8},
		{name: "a budget too odd for the next cluster stops short of overflowing", width: 7, want: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ansi.StringWidth(truncateToWidth(row, tc.width)); got != tc.want {
				t.Errorf("truncateToWidth(row, %d) display width = %d, want %d", tc.width, got, tc.want)
			}
		})
	}
}

func TestTruncateToWidthHandlesRowsWithNothingToShow(t *testing.T) {
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
			got := truncateToWidth(tc.row, 40)
			if w := ansi.StringWidth(got); w != 0 {
				t.Errorf("truncateToWidth(%q, 40) display width = %d, want 0", tc.row, w)
			}
			if stripSGR(got) != "" {
				t.Errorf("truncateToWidth(%q, 40) = %q, want no visible text", tc.row, got)
			}
		})
	}
}

func TestColoredLiveRowFitsTheTerminal(t *testing.T) {
	t.Setenv("COLUMNS", "40")

	drawnRow := func(colorEnabled bool) string {
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, true, colorEnabled)
		r.useClock(func() time.Time { return time.Unix(0, 0) })

		app := appStage(1)
		r.StagePlan(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
			{Id: app, Title: "a-long-application-name"},
		}})
		r.Progress(app, "uploading a great many static assets", 1, nil)
		_ = r.Close()

		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, "a-long-application-name") {
				return line
			}
		}
		t.Fatalf("output = %q, want a live row for the app", out.String())
		return ""
	}

	colored, plain := drawnRow(true), drawnRow(false)
	if got := ansi.StringWidth(colored); got > 39 {
		t.Errorf("live row display width = %d, want at most 39 so it cannot wrap in a 40-column terminal", got)
	}
	if got := stripSGR(colored); got != plain {
		t.Errorf("coloured live row shows %q, want the same text as the uncoloured row %q", got, plain)
	}
}
