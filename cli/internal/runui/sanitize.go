package runui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const tabStop = 8

var boxDrawing = &unicode.RangeTable{R16: []unicode.Range16{{Lo: 0x2500, Hi: 0x257f, Stride: 1}}}

func sanitize(raw string) (string, bool) {
	drafts := strings.Split(raw, "\r")
	for i := len(drafts) - 1; i >= 0; i-- {
		if text := visible(drafts[i]); strings.ContainsFunc(text, says) {
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}

func says(r rune) bool {
	return !unicode.IsSpace(r) && !unicode.In(r, boxDrawing)
}

func visible(draft string) string {
	var b strings.Builder
	for _, r := range ansi.Strip(draft) {
		switch {
		case r == '\t':
			b.WriteString(strings.Repeat(" ", tabStop-ansi.StringWidth(b.String())%tabStop))
		case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Variation_Selector):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
