package runui

import (
	"io"
	"os"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
)

func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func termWidth(w io.Writer) int {
	if n, ok := liveWidth(w); ok {
		return n
	}
	if n, ok := positiveEnvInt("COLUMNS"); ok {
		return n
	}
	return defaultWidth
}

func liveWidth(w io.Writer) (int, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, false
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return 0, false
	}
	return width, true
}

func termHeight(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if _, height, err := term.GetSize(int(f.Fd())); err == nil && height > 0 {
			return height
		}
	}
	if n, ok := positiveEnvInt("LINES"); ok {
		return n
	}
	return defaultHeight
}

func positiveEnvInt(name string) (int, bool) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func truncateToWidth(s string, width int) string {
	limit := width - 1
	if limit < 1 {
		limit = 1
	}
	return ansi.Truncate(s, limit, "")
}
