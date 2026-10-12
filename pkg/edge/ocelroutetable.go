package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const OcelRouteTableVersion = 1

type TrailingSlash string

const (
	TrailingSlashNever  TrailingSlash = "never"
	TrailingSlashAlways TrailingSlash = "always"
	TrailingSlashIgnore TrailingSlash = "ignore"
)

type OcelRouteTable struct {
	Version       int            `json:"version"`
	BasePath      string         `json:"basePath"`
	TrailingSlash TrailingSlash  `json:"trailingSlash"`
	Headers       []HeaderRule   `json:"headers"`
	Redirects     []RedirectRule `json:"redirects"`
	Routes        []Route        `json:"routes"`
	Fallback      Fallback       `json:"fallback"`
}

type HeaderRule struct {
	Src     string            `json:"src"`
	Headers map[string]string `json:"headers"`
}

type RedirectRule struct {
	Src      string `json:"src"`
	Location string `json:"location"`
	Status   int    `json:"status"`
}

type Route struct {
	Src      string     `json:"src"`
	Function string     `json:"function"`
	Cache    *CacheRule `json:"cache,omitempty"`
}

type Fallback struct {
	Function string `json:"function"`
}

type CacheRule struct {
	Revalidate  int      `json:"revalidate"`
	Expire      int      `json:"expire,omitempty"`
	AllowQuery  []string `json:"allowQuery,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	BypassToken string   `json:"bypassToken,omitempty"`
}

const (
	maxRepeat      = 1000
	minBypassToken = 16
)

var (
	headerName            = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	bypassTokenCharacters = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	groupRead             = regexp.MustCompile(`\$(\d+)`)
	repeatBound           = regexp.MustCompile(`^\{(\d+)(?:,(\d*))?\}`)
	absoluteLocation      = regexp.MustCompile(`(?i)^https?://`)
)

func ReadOcelRouteTable(raw []byte) (OcelRouteTable, error) {
	var routes OcelRouteTable
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&routes); err != nil {
		return OcelRouteTable{}, fmt.Errorf("parse the route table: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return OcelRouteTable{}, errors.New("parse the route table: it holds more than one JSON document")
	}
	if err := routes.refuse(); err != nil {
		return OcelRouteTable{}, err
	}
	return routes, nil
}

func (r OcelRouteTable) HasCachedRoute() bool {
	return slices.ContainsFunc(r.Routes, func(route Route) bool { return route.Cache != nil })
}

func (r OcelRouteTable) Functions() []string {
	functions := []string{r.Fallback.Function}
	for _, route := range r.Routes {
		functions = append(functions, route.Function)
	}
	slices.Sort(functions)
	return slices.Compact(functions)
}

func (r OcelRouteTable) refuse() error {
	switch {
	case r.Version != OcelRouteTableVersion:
		return fmt.Errorf("the route table is version %d, and this CLI reads version %d", r.Version, OcelRouteTableVersion)
	case r.BasePath != "" && (!strings.HasPrefix(r.BasePath, "/") || strings.HasSuffix(r.BasePath, "/") || strings.ContainsAny(r.BasePath, "?#")):
		return fmt.Errorf("the route table's basePath %q is empty, or starts with a slash, ends in none and names no query or fragment", r.BasePath)
	case !slices.Contains([]TrailingSlash{TrailingSlashNever, TrailingSlashAlways, TrailingSlashIgnore}, r.TrailingSlash):
		return fmt.Errorf("the route table's trailingSlash is %q, and it is never, always or ignore", r.TrailingSlash)
	case r.Headers == nil || r.Redirects == nil || r.Routes == nil:
		return errors.New("the route table states headers, redirects and routes, each a list, empty if need be")
	case r.Fallback.Function == "":
		return errors.New("the route table's fallback names no function")
	}
	for i, rule := range r.Headers {
		if err := rule.refuse(); err != nil {
			return fmt.Errorf("header rule %d (%q): %w", i, rule.Src, err)
		}
	}
	for i, rule := range r.Redirects {
		if err := rule.refuse(); err != nil {
			return fmt.Errorf("redirect %d (%q): %w", i, rule.Src, err)
		}
	}
	return r.refuseRoutes()
}

func (r OcelRouteTable) refuseRoutes() error {
	var token string
	for i, route := range r.Routes {
		if err := route.refuse(); err != nil {
			return fmt.Errorf("route %d (%q): %w", i, route.Src, err)
		}
		if route.Cache == nil || route.Cache.BypassToken == "" {
			continue
		}
		if token != "" && token != route.Cache.BypassToken {
			return fmt.Errorf("route %d (%q): a table has one bypassToken, and this route states another", i, route.Src)
		}
		token = route.Cache.BypassToken
	}
	return nil
}

func (r HeaderRule) refuse() error {
	if _, err := compilePattern(r.Src); err != nil {
		return err
	}
	if len(r.Headers) == 0 {
		return errors.New("a header rule sets at least one header")
	}
	for name, value := range r.Headers {
		if !headerName.MatchString(name) {
			return fmt.Errorf("%q is not a header name", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("the value of %s holds a line break", name)
		}
	}
	return nil
}

func (r RedirectRule) refuse() error {
	compiled, err := compilePattern(r.Src)
	if err != nil {
		return err
	}
	if !slices.Contains([]int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect}, r.Status) {
		return fmt.Errorf("status %d is not 301, 302, 307 or 308", r.Status)
	}
	if r.Location == "" {
		return errors.New("a redirect names a location")
	}
	if !absoluteLocation.MatchString(r.Location) && !isSameSiteLocation(r.Location) {
		return errors.New("a location is a path that begins with one slash, or an http or https URL")
	}
	for _, read := range groupRead.FindAllStringSubmatch(r.Location, -1) {
		if group, _ := strconv.Atoi(read[1]); group < 1 || group > compiled.NumSubexp() {
			return fmt.Errorf("the location reads $%s, and the pattern has %d groups", read[1], compiled.NumSubexp())
		}
	}
	return nil
}

func isSameSiteLocation(location string) bool {
	return strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "//") && !strings.HasPrefix(location, `/\`)
}

func (r Route) refuse() error {
	if _, err := compilePattern(r.Src); err != nil {
		return err
	}
	if r.Function == "" {
		return errors.New("a route names a function")
	}
	if r.Cache == nil {
		return nil
	}
	return r.Cache.refuse()
}

func (c CacheRule) refuse() error {
	switch {
	case c.Revalidate < 1:
		return errors.New("revalidate is a whole number of seconds, at least 1")
	case c.Expire != 0 && c.Expire <= c.Revalidate:
		return errors.New("expire is a whole number of seconds longer than revalidate")
	case slices.Contains(c.AllowQuery, ""):
		return errors.New("allowQuery names no empty parameter")
	case slices.Contains(c.Tags, ""):
		return errors.New("a tag is not empty")
	case c.BypassToken != "" && (len(c.BypassToken) < minBypassToken || !bypassTokenCharacters.MatchString(c.BypassToken)):
		return fmt.Errorf("bypassToken is at least %d letters, digits, dashes or underscores", minBypassToken)
	}
	return nil
}

func anchorPattern(src string) string {
	return "^(?:" + src + ")$"
}

func compilePattern(src string) (*regexp.Regexp, error) {
	if err := refuseUnsharedSyntax(src); err != nil {
		return nil, err
	}
	compiled, err := regexp.Compile(anchorPattern(src))
	if err != nil {
		return nil, fmt.Errorf("the pattern does not compile: %w", err)
	}
	return compiled, nil
}

func refuseUnsharedSyntax(src string) error {
	if src == "" {
		return errors.New("a pattern is not empty")
	}
	inClass := false
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\\':
			i++
			if i == len(src) {
				return errors.New("a pattern does not end in a backslash")
			}
			if err := refuseEscape(src[i], inClass); err != nil {
				return err
			}
		case inClass:
			if c == '[' && strings.HasPrefix(src[i:], "[:") {
				return errors.New("a pattern names no POSIX class")
			}
			inClass = c != ']'
		case c == '[':
			inClass = true
			if rest := strings.TrimPrefix(src[i+1:], "^"); strings.HasPrefix(rest, "]") {
				return errors.New("a pattern names no empty or ]-first class")
			}
		case c == '(' && strings.HasPrefix(src[i:], "(?") && !strings.HasPrefix(src[i:], "(?:"):
			return errors.New("a pattern groups with ( or (?:, and uses no lookaround, named group or flag")
		case c == '{':
			if bounds := repeatBound.FindStringSubmatch(src[i:]); bounds != nil && repeatsPast(bounds[1:]) {
				return fmt.Errorf("a pattern repeats at most %d times", maxRepeat)
			}
		}
	}
	return nil
}

func repeatsPast(bounds []string) bool {
	for _, bound := range bounds {
		if n, err := strconv.Atoi(bound); err == nil && n > maxRepeat {
			return true
		}
	}
	return false
}

func refuseEscape(c byte, inClass bool) error {
	switch {
	case strings.IndexByte("dDwWnrt", c) >= 0:
		return nil
	case strings.IndexByte("bB", c) >= 0 && !inClass:
		return nil
	case c < 0x80 && !isAlphanumeric(c):
		return nil
	}
	return fmt.Errorf("a pattern escapes with \\d, \\D, \\w, \\W, \\n, \\r, \\t, \\b, \\B or a punctuation mark, and \\%c is none", c)
}

func isAlphanumeric(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
