package terminal

import (
	"fmt"
	"io"
	"strings"
)

func PrintFailure(w io.Writer, err error) {
	for _, line := range failureLines(PaletteFor(w), "", err.Error()) {
		fmt.Fprintln(w, line)
	}
}

func failureLines(p Palette, headline, detail string) []string {
	lines := strings.Split(strings.TrimRight(detail, "\n"), "\n")
	head := failGlyph + " " + headline
	switch {
	case headline == "":
		head += lines[0]
	case lines[0] != "":
		head += " — " + lines[0]
	}
	out := []string{p.FailureBold(head)}
	for _, line := range lines[1:] {
		out = append(out, blockIndent+line)
	}
	return out
}
