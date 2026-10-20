package credential

import (
	"strings"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

type Resource struct {
	Noun string
	Name string
}

func (r Resource) String() string { return r.Noun + " " + r.Name }

type Grant struct {
	Role        string
	Permissions []string
	On          Resource
	Condition   *policy.Condition
	Why         string
}

func (g Grant) String() string {
	line := g.Role
	if line == "" {
		line = "a custom role holding " + strings.Join(g.Permissions, ", ")
	}
	switch {
	case g.On.Name != "":
		line += ", on " + g.On.String()
	case g.Condition != nil:
		line += ", on the condition " + g.Condition.Expression
	default:
		line += ", unconditioned"
	}
	if g.Why != "" {
		line += ": " + g.Why
	}
	return line
}

func Lines(grants []Grant) []string {
	lines := make([]string, len(grants))
	for i, grant := range grants {
		lines[i] = grant.String()
	}
	return lines
}
