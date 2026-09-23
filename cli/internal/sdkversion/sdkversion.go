package sdkversion

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
)

const (
	JS     = "js"
	Go     = "go"
	Python = "python"
	Rust   = "rust"
)

type release struct {
	major, minor, patch int
	pre                 string
}

const core = `(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)`

var (
	stable    = regexp.MustCompile(`^` + core + `$`)
	candidate = regexp.MustCompile(`^` + core + `(?:-rc\.|rc)([1-9]\d*)$`)
	nightly   = regexp.MustCompile(`^` + core + `(?:-0\.nightly\.(\d{8})\.g[0-9a-f]{7}|\.dev(\d{8}))$`)
)

func parse(version string) (release, bool) {
	version = strings.TrimPrefix(version, "v")
	var pre string
	match := stable.FindStringSubmatch(version)
	if match == nil {
		match = candidate.FindStringSubmatch(version)
		if match != nil {
			pre = "rc" + match[4]
		}
	}
	if match == nil {
		match = nightly.FindStringSubmatch(version)
		if match != nil {
			pre = "dev" + match[4] + match[5]
		}
	}
	if match == nil {
		return release{}, false
	}
	numbers := make([]int, 3)
	for i := range numbers {
		numbers[i], _ = strconv.Atoi(match[i+1])
	}
	r := release{major: numbers[0], minor: numbers[1], patch: numbers[2], pre: pre}
	if r.major == 0 && r.minor == 0 && r.patch == 0 {
		return release{}, false
	}
	return r, true
}

func Released(version string) bool {
	_, ok := parse(version)
	return ok
}

func Compatible(cli, sdk string) bool {
	c, known := parse(cli)
	s, alsoKnown := parse(sdk)
	if !known || !alsoKnown {
		return true
	}
	switch {
	case c.major == 0 && c.minor == 0:
		return c == s
	case c.major == 0:
		return s.major == 0 && s.minor == c.minor
	default:
		return s.major == c.major
	}
}

func Upgrade(language, cli string) string {
	switch language {
	case JS:
		return "npm i ocel@" + cli
	case Python:
		return "uv add ocel==" + pep440(cli)
	case Rust:
		return "cargo add ocel-sdk@" + cli
	case Go:
		return "go get ocel.dev@v" + strings.TrimPrefix(cli, "v")
	}
	return ""
}

func pep440(version string) string {
	r, ok := parse(version)
	if !ok {
		return version
	}
	base := fmt.Sprintf("%d.%d.%d", r.major, r.minor, r.patch)
	switch {
	case strings.HasPrefix(r.pre, "rc"):
		return base + r.pre
	case strings.HasPrefix(r.pre, "dev"):
		return base + "." + r.pre
	}
	return base
}

var names = map[string]string{
	JS:     "the JavaScript SDK (ocel)",
	Python: "the Python SDK (ocel)",
	Rust:   "the Rust SDK (ocel-sdk)",
	Go:     "the Go SDK (ocel.dev)",
}

type MismatchError struct {
	Language string
	SDK      string
	CLI      string
}

func Name(language string) string {
	if named, ok := names[language]; ok {
		return named
	}
	return "the " + language + " SDK"
}

func (e *MismatchError) Error() string {
	named := Name(e.Language)
	said := fmt.Sprintf("%s is version %s and this CLI is version %s; an SDK works with the CLI of its own release", named, e.SDK, e.CLI)
	if upgrade := Upgrade(e.Language, e.CLI); upgrade != "" {
		said += " — run `" + upgrade + "`"
	}
	return said
}

func Check(language, sdk, cli string) error {
	if Compatible(cli, sdk) {
		return nil
	}
	return &MismatchError{Language: language, SDK: sdk, CLI: cli}
}

func Format(language, version string) string {
	return language + "/" + version
}

func Parse(header string) (language, version string, ok bool) {
	language, version, ok = strings.Cut(header, "/")
	return language, version, ok && language != ""
}

type Gate struct {
	cli string

	mu      sync.Mutex
	refused error
}

func NewGate(cli string) *Gate {
	return &Gate{cli: cli}
}

func (g *Gate) Interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if err := g.check(req.Header().Get(constants.SDKVersionHeader)); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			return next(ctx, req)
		}
	})
}

func (g *Gate) check(header string) error {
	language, version, ok := Parse(header)
	if !ok {
		return nil
	}
	err := Check(language, version, g.cli)
	if err == nil {
		return nil
	}
	g.mu.Lock()
	if g.refused == nil {
		g.refused = err
	}
	g.mu.Unlock()
	return err
}

func (g *Gate) Take() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	refused := g.refused
	g.refused = nil
	return refused
}
