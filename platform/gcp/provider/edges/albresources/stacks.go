package albresources

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func LoadBalancerStack(tier environment.Tier) string {
	return dashed(string(Kind), "front", naming.TierWord(tier))
}

func BindingStack(slug string, tier environment.Tier) string {
	return dashed(string(Kind), naming.TierWord(tier), naming.Sanitize(slug))
}

func GateStack(tier environment.Tier) string {
	return dashed(string(Kind), "gate", naming.TierWord(tier))
}

func ListTierProjects(tier environment.Tier) []string {
	return []string{
		naming.PulumiProject(LoadBalancerStack(tier)),
		naming.PulumiProject(GateStack(tier)),
	}
}

func dashed(parts ...string) string { return strings.Join(parts, "-") }

type Stack struct {
	Tier environment.Tier
	Slug string
	Gate bool
}

func (s Stack) Name() string {
	switch {
	case s.Gate:
		return GateStack(s.Tier)
	case s.Slug != "":
		return BindingStack(s.Slug, s.Tier)
	}
	return LoadBalancerStack(s.Tier)
}

func (s Stack) Prefix() string {
	if s.Slug == "" {
		return s.Name()
	}
	return strings.Join([]string{string(Kind), "project", naming.Sanitize(s.Slug)}, "-")
}
