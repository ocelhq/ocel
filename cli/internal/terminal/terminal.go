package terminal

import (
	"io"
	"os"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

const defaultColumns = 80

type File interface {
	io.Writer
	Fd() uintptr
}

func IsTerminal(v any) bool {
	f, ok := v.(File)
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
	return defaultColumns
}

func liveWidth(w io.Writer) (int, bool) {
	f, ok := w.(File)
	if !ok {
		return 0, false
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return 0, false
	}
	return width, true
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

func fitToWidth(s string, columns int) string {
	if columns < 1 {
		return ""
	}
	fitted := ansi.Truncate(s, columns, "")
	for cut := columns - 1; cut >= 0 && ansi.StringWidthWc(fitted) > columns; cut-- {
		fitted = ansi.Truncate(s, cut, "")
	}
	return fitted
}

func displayWidth(s string) int {
	return max(ansi.StringWidth(s), ansi.StringWidthWc(s))
}
