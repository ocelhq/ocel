package origin

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type Source string

const (
	SourceConfigured Source = "configured"
	SourceDefault    Source = "default"
)

type Fill struct {
	Role     Role
	Name     string
	Kind     Kind
	Source   Source
	Reach    Reach
	Protocol Protocol
	Native   bool
}

type Refusal struct {
	Role   Role
	Name   string
	Reason string
	Fixes  []Kind
}

func (r Refusal) Error() string {
	if len(r.Fixes) == 0 {
		return r.Reason
	}
	return fmt.Sprintf("%s; %s fills it: %s", r.Reason, r.Role, joinKinds(r.Fixes))
}

type Plan struct {
	Fills    []Fill
	Refusals []Refusal
}

func (p Plan) OK() bool { return len(p.Refusals) == 0 }

func Resolve(c Catalog, t Topology, req Requirements) Plan {
	var p Plan
	if t.Origin.Kind != c.Origin.Kind {
		p.Refusals = append(p.Refusals, Refusal{Role: RoleOrigin, Reason: fmt.Sprintf("this binary is the %s origin, not %s", c.Origin.Kind, t.Origin.Kind)})
		return p
	}
	p.Fills = append(p.Fills, Fill{Role: RoleOrigin, Kind: c.Origin.Kind, Source: SourceConfigured, Native: true})

	p.fill(c, RoleEdge, "", t.Edge, c.Origin.DefaultEdge, true)
	if t.DNS.Kind != "" {
		p.fill(c, RoleDNS, "", t.DNS, "", false)
	}

	for _, r := range req.Resources {
		p.fill(c, r.Role, r.Name, t.Resources[r.Role], c.Origin.Defaults[r.Role], false)
	}
	return p
}

func (p *Plan) fill(c Catalog, role Role, name string, chosen Selection, fallback Kind, required bool) {
	kind, source := chosen.Kind, SourceConfigured
	if kind == "" {
		kind, source = fallback, SourceDefault
	}
	if kind == "" {
		if !required && role == RoleDNS {
			return
		}
		p.Refusals = append(p.Refusals, Refusal{Role: role, Name: name,
			Reason: fmt.Sprintf("the %s origin has no default %s and none is configured", c.Origin.Kind, role),
			Fixes:  p.fixes(c, role)})
		return
	}
	facts, ok := c.Backings[role][kind]
	if !ok {
		p.Refusals = append(p.Refusals, Refusal{Role: role, Name: name,
			Reason: fmt.Sprintf("no %s backing of kind %q is linked into the %s origin", role, kind, c.Origin.Kind),
			Fixes:  p.fixes(c, role)})
		return
	}
	if facts.Native != "" && facts.Native != c.Origin.Kind {
		p.Refusals = append(p.Refusals, Refusal{Role: role, Name: name,
			Reason: fmt.Sprintf("%s is native to the %s origin and cannot back %s on %s", kind, facts.Native, role, c.Origin.Kind),
			Fixes:  p.fixes(c, role)})
		return
	}
	reach, _ := ReachOf(role)
	if reach == ReachMembrane && !slices.Contains(c.Origin.Speaks, facts.Protocol) {
		p.Refusals = append(p.Refusals, Refusal{Role: role, Name: name,
			Reason: fmt.Sprintf("%s reaches the app through the membrane over %s, and the %s runtime speaks only %s", kind, facts.Protocol, c.Origin.Kind, joinProtocols(c.Origin.Speaks)),
			Fixes:  p.fixes(c, role)})
		return
	}
	p.Fills = append(p.Fills, Fill{Role: role, Name: name, Kind: kind, Source: source, Reach: reach, Protocol: facts.Protocol, Native: facts.Native != ""})
}

func (p *Plan) fixes(c Catalog, role Role) []Kind {
	var out []Kind
	reach, _ := ReachOf(role)
	for kind, f := range c.Backings[role] {
		if f.Native != "" && f.Native != c.Origin.Kind {
			continue
		}
		if reach == ReachMembrane && !slices.Contains(c.Origin.Speaks, f.Protocol) {
			continue
		}
		out = append(out, kind)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func joinKinds(kinds []Kind) string {
	s := make([]string, len(kinds))
	for i, k := range kinds {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

func joinProtocols(ps []Protocol) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}
