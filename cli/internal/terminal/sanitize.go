package terminal

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const tabStopColumns = 8

var boxDrawing = &unicode.RangeTable{R16: []unicode.Range16{{Lo: 0x2500, Hi: 0x257f, Stride: 1}}}

func sanitize(raw string) (string, bool) {
	drafts := strings.Split(raw, "\r")
	for i := len(drafts) - 1; i >= 0; i-- {
		if text := stripInvisible(drafts[i]); strings.ContainsFunc(text, isText) {
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}

func isText(r rune) bool {
	return !unicode.IsSpace(r) && !unicode.In(r, boxDrawing)
}

func stripInvisible(draft string) string {
	var b strings.Builder
	for _, r := range ansi.Strip(draft) {
		switch {
		case r == '\t':
			b.WriteString(strings.Repeat(" ", tabStopColumns-ansi.StringWidth(b.String())%tabStopColumns))
		case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Variation_Selector):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var selectGraphicRendition = regexp.MustCompile(`^(\x1b\[|\x9b)[0-9;:]*m$`)

func keepColour(raw string) string {
	var b strings.Builder
	var state byte
	for rest := raw; rest != ""; {
		seq, _, n, next := ansi.DecodeSequence(rest, state, nil)
		state, rest = next, rest[n:]
		if escapes := seq[0] == ansi.ESC || seq[0] >= 0x80 && seq[0] <= 0x9f; !escapes || selectGraphicRendition.MatchString(seq) {
			b.WriteString(seq)
		}
	}
	return b.String()
}

const grayForeground = "\x1b[90m"

func mutedToolText(text string) string {
	if text == "" {
		return text
	}
	var b strings.Builder
	b.WriteString(grayForeground)
	var state byte
	for rest := text; rest != ""; {
		seq, _, n, next := ansi.DecodeSequence(rest, state, nil)
		state, rest = next, rest[n:]
		b.WriteString(seq)
		if selectGraphicRendition.MatchString(seq) && resetsForeground(seq) {
			b.WriteString(grayForeground)
		}
	}
	b.WriteString("\x1b[39m")
	return b.String()
}

func resetsForeground(seq string) bool {
	params := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(seq, "\x1b["), "\x9b"), "m")
	if params == "" {
		return true
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "", "0", "39":
			return true
		case "38", "48", "58":
			if i+1 < len(fields) && fields[i+1] == "5" {
				i += 2
			} else if i+1 < len(fields) && fields[i+1] == "2" {
				i += 4
			}
		}
	}
	return false
}
