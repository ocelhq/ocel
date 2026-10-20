package credential

import (
	"strings"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

type Grant struct {
	Role        string
	Permissions []string
	On          string
	Condition   *policy.Condition
	Why         string
}

func (g Grant) String() string {
	line := g.Role
	if line == "" {
		line = "a custom role holding " + strings.Join(g.Permissions, ", ")
	}
	switch {
	case g.On != "":
		line += ", on " + resourceNamed(g.On)
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

func resourceNamed(name string) string {
	switch {
	case strings.Contains(name, "/queues/"):
		return "the queue " + name
	case !strings.HasPrefix(name, "projects/") && strings.Count(name, "/") == 2:
		return "the tag value " + name
	}
	return name
}
