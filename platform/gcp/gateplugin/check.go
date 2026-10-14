package gateplugin

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

const cookieHeader = "cookie"

func ReadRequest(pairs [][2]string) gate.Request {
	req := gate.Request{Header: gate.Header{}}
	for _, pair := range pairs {
		name, value := pair[0], pair[1]
		switch {
		case name == ":method":
			req.Method = value
		case name == ":path":
			req.Target = value
		case name == ":authority":
			req.Host = value
		case strings.HasPrefix(name, ":"):
		case strings.EqualFold(name, cookieHeader):
			for _, line := range splitJoinedCookies(value) {
				req.Header.Add(name, line)
			}
		default:
			req.Header.Add(name, value)
		}
	}
	return req
}

func splitJoinedCookies(value string) []string {
	var lines []string
	start := 0
	for i := 0; i+2 < len(value); i++ {
		if value[i] == ',' && value[i+1] == ' ' && startsCookie(value[i+2:]) {
			lines = append(lines, value[start:i])
			start = i + 2
		}
	}
	return append(lines, value[start:])
}

func startsCookie(rest string) bool {
	for i := 0; i < len(rest); i++ {
		switch b := rest[i]; {
		case b == '=':
			return i > 0
		case isCookieNameByte(b):
		default:
			return false
		}
	}
	return false
}

func isCookieNameByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || b != 0 && b < 0x7f && strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0
}

func (c Config) Check(req gate.Request, now time.Time) gate.Verdict {
	held, gated := c.FindGate(req.Host)
	if !gated {
		return gate.Verdict{Forward: req.Header}
	}
	return held.Check(req, now)
}

func ListHeaderEdits(original, forward gate.Header) (remove []string, replace map[string]string) {
	replace = map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(original)) {
		values := forward.Values(name)
		switch {
		case len(values) == 0:
			remove = append(remove, strings.ToLower(name))
		case !slices.Equal(values, original[name]) && changesCookie(name, original[name]):
			replace[strings.ToLower(name)] = strings.Join(values, "; ")
		case !slices.Equal(values, original[name]) && !strings.EqualFold(name, cookieHeader):
			replace[strings.ToLower(name)] = strings.Join(values, ", ")
		}
	}
	return remove, replace
}

func changesCookie(name string, values []string) bool {
	return strings.EqualFold(name, cookieHeader) && strings.Contains(strings.Join(values, ";"), gate.CookieName+"=")
}
