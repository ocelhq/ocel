package stackrecords

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/records"
)

func rooted(root string, tier environment.Tier, rest ...string) records.Name {
	return append(records.Name{root, string(tier)}, rest...)
}

func SchemaRecord(tier environment.Tier) records.Name { return rooted(records.RootSchema, tier) }

func ProjectsRecord(tier environment.Tier) records.Name { return rooted(records.RootProjects, tier) }

func ProjectRecord(tier environment.Tier, slug string) records.Name {
	return rooted(records.RootProjects, tier, slug)
}

func BootstrapRecord(tier environment.Tier) records.Name {
	return rooted(records.RootBootstrap, tier)
}

func StackRecord(tier environment.Tier, slug string, stack naming.StackName) records.Name {
	return append(StacksRecord(tier, slug), stack.String())
}

func StacksRecord(tier environment.Tier, slug string) records.Name {
	return rooted(records.RootStacks, tier, slug)
}

func EnvironmentsRecord(tier environment.Tier, slug string) records.Name {
	return rooted(records.RootEnvironments, tier, slug)
}

func EnvironmentRecord(tier environment.Tier, slug, env string) records.Name {
	return append(EnvironmentsRecord(tier, slug), env)
}

func EdgeStackRecord(tier environment.Tier, slug string) records.Name {
	return rooted(records.RootEdgeStacks, tier, slug)
}

func EdgeStacksRecord(tier environment.Tier) records.Name {
	return rooted(records.RootEdgeStacks, tier)
}

func WildcardRecord(tier environment.Tier) records.Name { return rooted(records.RootWildcard, tier) }

func LedgerRecord(scope string, rest ...string) records.Name {
	return append(records.Name{records.RootLedger, scope}, rest...)
}

var tierSegment = map[string]int{
	records.RootSchema:             1,
	records.RootProjects:           1,
	records.RootStacks:             1,
	records.RootEnvironments:       1,
	records.RootBootstrap:          1,
	records.RootEdgeStacks:         1,
	records.RootWildcard:           1,
	records.RootLedger:             1,
	records.RootConformance:        1,
	records.RootValueRefs:          1,
	records.RootEnvSources:         1,
	records.RootEnvSourceStatus:    1,
	records.RootEnvSourceDigestKey: 1,
	records.RootValues:             2,
}

func TierOf(name records.Name) (environment.Tier, bool) {
	if len(name) == 0 {
		return "", false
	}
	at, named := tierSegment[name[0]]
	if !named || len(name) <= at {
		return "", false
	}
	segment := name[at]
	if name[0] == records.RootLedger {
		segment, _, _ = strings.Cut(segment, naming.PathSeparator)
	}
	switch environment.Tier(segment) {
	case environment.TierProduction, environment.TierPreview:
		return environment.Tier(segment), true
	}
	return "", false
}
