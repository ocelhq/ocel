package stackrecords

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func rooted(root string, class edge.Class, rest ...string) records.Name {
	return append(records.Name{root, string(class)}, rest...)
}

func SchemaRecord(class edge.Class) records.Name { return rooted(records.RootSchema, class) }

func ProjectsRecord(class edge.Class) records.Name { return rooted(records.RootProjects, class) }

func ProjectRecord(class edge.Class, slug string) records.Name {
	return rooted(records.RootProjects, class, slug)
}

func BootstrapRecord(class edge.Class) records.Name { return rooted(records.RootBootstrap, class) }

func StackRecord(class edge.Class, slug string, stack naming.StackName) records.Name {
	return append(StacksRecord(class, slug), stack.String())
}

func StacksRecord(class edge.Class, slug string) records.Name {
	return rooted(records.RootStacks, class, slug)
}

func EnvironmentsRecord(class edge.Class, slug string) records.Name {
	return rooted(records.RootEnvironments, class, slug)
}

func EnvironmentRecord(class edge.Class, slug, env string) records.Name {
	return append(EnvironmentsRecord(class, slug), env)
}

func EdgeStackRecord(class edge.Class, slug string) records.Name {
	return rooted(records.RootEdgeStacks, class, slug)
}

func EdgeStacksRecord(class edge.Class) records.Name {
	return rooted(records.RootEdgeStacks, class)
}

func WildcardRecord(class edge.Class) records.Name { return rooted(records.RootWildcard, class) }

func LedgerRecord(scope string, rest ...string) records.Name {
	return append(records.Name{records.RootLedger, scope}, rest...)
}

var classSegment = map[string]int{
	records.RootSchema:       1,
	records.RootProjects:     1,
	records.RootStacks:       1,
	records.RootEnvironments: 1,
	records.RootBootstrap:    1,
	records.RootEdgeStacks:   1,
	records.RootWildcard:     1,
	records.RootLedger:       1,
	records.RootConformance:  1,
	records.RootValueRefs:    1,
	records.RootValues:       2,
}

func ClassOf(name records.Name) (edge.Class, bool) {
	if len(name) == 0 {
		return "", false
	}
	at, named := classSegment[name[0]]
	if !named || len(name) <= at {
		return "", false
	}
	segment := name[at]
	if name[0] == records.RootLedger {
		segment, _, _ = strings.Cut(segment, naming.PathSeparator)
	}
	switch edge.Class(segment) {
	case edge.ClassProduction, edge.ClassPreview:
		return edge.Class(segment), true
	}
	return "", false
}

type Project struct {
	Features []string `json:"features,omitempty"`
}

type BootstrapSettings struct {
	AutoHeal bool `json:"auto_heal,omitempty"`
}
