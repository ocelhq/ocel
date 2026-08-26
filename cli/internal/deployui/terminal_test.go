package deployui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTruncateToWidthIgnoresColorCodes(t *testing.T) {
	t.Parallel()

	plain := "deploying the application"
	colored := "\x1b[32m" + plain + "\x1b[0m"

	for width := 4; width <= len(plain)+4; width++ {
		wantWidth := ansi.StringWidth(truncateToWidth(plain, width))
		if got := ansi.StringWidth(truncateToWidth(colored, width)); got != wantWidth {
			t.Errorf("truncateToWidth(colored, %d) display width = %d, want %d", width, got, wantWidth)
		}
	}
}

func TestTruncateToWidthReservesOneColumn(t *testing.T) {
	t.Parallel()

	row := "\x1b[32m" + strings.Repeat("a", 40) + "\x1b[0m"

	cases := []struct {
		width int
		want  int
	}{
		{width: 42, want: 40},
		{width: 41, want: 40},
		{width: 40, want: 39},
		{width: 2, want: 1},
		{width: 1, want: 1},
		{width: 0, want: 1},
	}
	for _, tc := range cases {
		if got := ansi.StringWidth(truncateToWidth(row, tc.width)); got != tc.want {
			t.Errorf("truncateToWidth(row, %d) display width = %d, want %d", tc.width, got, tc.want)
		}
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

	sgr := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	row := "ab\x1b[31mcd\x1b[0m"
	for width := 1; width <= 8; width++ {
		got := truncateToWidth(row, width)
		if stripped := sgr.ReplaceAllString(got, ""); strings.Contains(stripped, "\x1b") {
			t.Errorf("truncateToWidth(row, %d) = %q, want no split escape sequence", width, got)
		}
	}
}

func TestTruncateToWidthCountsWideRunes(t *testing.T) {
	t.Parallel()

	row := "\x1b[36m日本語テキスト\x1b[0m"
	if got := ansi.StringWidth(truncateToWidth(row, 9)); got != 8 {
		t.Errorf("truncateToWidth(row, 9) display width = %d, want 8", got)
	}
}
