package naming

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const minCutFieldChars = 3

type PhysicalName struct {
	Fence     string
	Qualifier string
	Project   string
	Env       string
	App       string
	Declared  string
	Kind      string
	Tag       string
}

func (c Coordinate) AppResourceName(namespace, tag string) PhysicalName {
	return PhysicalName{Fence: AppFence(namespace), Project: c.Project, Env: c.Env, App: c.App, Declared: c.Name, Tag: tag}
}

func (c Coordinate) TaggedResourceName(namespace string) PhysicalName {
	tag := c.Release.String()
	if c.Release.IsZero() {
		tag = c.HashTag(namespace)
	}
	return c.AppResourceName(namespace, tag)
}

func (p PhysicalName) Fit(limit int, childSuffixes ...string) (string, error) {
	return p.fit(limit, longestChildSuffix(childSuffixes), true)
}

func (p PhysicalName) FitUntagged(limit int, childSuffixes ...string) (string, error) {
	p.Tag = ""
	return p.fit(limit, longestChildSuffix(childSuffixes)+len(WordSeparator)+tagChars, false)
}

func (p PhysicalName) fit(limit, reserved int, tagged bool) (string, error) {
	required := []struct{ field, value string }{{"project", p.Project}, {"env", p.Env}}
	if tagged {
		required = append(required, struct{ field, value string }{"tag", p.Tag})
	}
	for _, required := range required {
		if required.value == "" {
			return "", fmt.Errorf("a physical name needs a %s", required.field)
		}
	}
	project, env, app := Sanitize(p.Project), Sanitize(p.Env), appField(p.App)
	if app != "" {
		app = Sanitize(app)
	}
	if app == "" && p.Declared == "" {
		return "", fmt.Errorf("the physical name in %s/%s needs a declared name or an app", p.Project, p.Env)
	}
	name := func() string {
		return p.Fence + Join(WordSeparator, p.Qualifier, project, env, app, p.Declared, p.Kind, p.Tag)
	}
	full := name()
	for _, field := range []*string{&env, &project, &app} {
		over := len(name()) + reserved - limit
		if over <= 0 {
			break
		}
		keep := max(len(*field)-over, min(len(*field), minCutFieldChars))
		*field = strings.TrimRight((*field)[:keep], WordSeparator)
	}
	if fitted := name(); len(fitted)+reserved <= limit {
		return fitted, nil
	}
	kept := ""
	if reserved > 0 {
		kept = fmt.Sprintf(" with %d kept for what is appended to it", reserved)
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"%s does not fit %d characters%s, even with env, project and app cut to %d characters each\nShorten %s",
		full, limit, kept, minCutFieldChars, cmp.Or(p.Declared, p.App))
}

func longestChildSuffix(childSuffixes []string) int {
	longest := 0
	for _, suffix := range childSuffixes {
		longest = max(longest, len(WordSeparator)+len(Sanitize(suffix)))
	}
	return longest
}
