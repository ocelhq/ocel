package docker

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	NameMax          = 63
	minCutFieldChars = 3
)

type Name struct {
	Project  string
	Env      string
	App      string
	Declared string
	Kind     string
	Tag      string
}

func (n Name) Fit() (string, error) {
	for field, value := range map[string]string{"project": n.Project, "env": n.Env, "tag": n.Tag} {
		if value == "" {
			return "", fmt.Errorf("a name on the box needs a %s", field)
		}
	}
	project, env, app := naming.Sanitize(n.Project), naming.Sanitize(n.Env), ""
	if n.App != "" {
		app = naming.Sanitize(n.App)
	}
	name := func() string {
		return naming.Join(naming.WordSeparator, project, env, app, n.Declared, n.Kind, n.Tag)
	}
	full := name()
	for _, field := range []*string{&env, &project, &app} {
		over := len(name()) - NameMax
		if over <= 0 {
			break
		}
		keep := max(len(*field)-over, min(len(*field), minCutFieldChars))
		*field = strings.TrimRight((*field)[:keep], naming.WordSeparator)
	}
	if fitted := name(); len(fitted) <= NameMax {
		return fitted, nil
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"%s does not fit the %d characters a name on the box resolves within, even with env, project and app cut to %d characters each\nShorten %s", full, NameMax, minCutFieldChars, n.Declared)
}
