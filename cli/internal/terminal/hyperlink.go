package terminal

import (
	"net/url"
	"strings"
)

func (p Presentation) Hyperlink(text, target string) string {
	if !p.Hyperlinks || !isLinkable(target) {
		return text
	}
	return "\x1b]8;;" + target + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func isLinkable(target string) bool {
	if strings.ContainsFunc(target, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	parsed, err := url.Parse(target)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}
