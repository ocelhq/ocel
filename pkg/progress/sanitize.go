package progress

import "strings"

const MaxSpanNameLen = 200

func SanitizeSpanName(name string) string {
	out := stripControlChars(name, MaxSpanNameLen)
	if out == "" {
		return "span"
	}
	return out
}

func SanitizeMessage(message string) string {
	return stripControlChars(message, 0)
}

func stripControlChars(s string, maxLen int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if maxLen > 0 && b.Len() >= maxLen {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
