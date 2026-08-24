package providerkit

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	rootSchema     = "schema"
	rootProjects   = "projects"
	rootBootstrap  = "bootstrap"
	rootEdgeStacks = "edgestacks"
	rootWildcard   = "wildcard"
	rootLedger     = "ledger"
)

func rooted(root string, class Class, rest ...string) RecordName {
	return append(RecordName{root, string(class)}, rest...)
}

func SchemaRecord(class Class) RecordName { return rooted(rootSchema, class) }

func ProjectsRecord(class Class) RecordName { return rooted(rootProjects, class) }

func ProjectRecord(class Class, slug string) RecordName {
	return rooted(rootProjects, class, slug)
}

func BootstrapRecord(class Class) RecordName { return rooted(rootBootstrap, class) }

func StackRecord(class Class, slug string, stack naming.StackName) RecordName {
	return append(StacksRecord(class, slug), stack.String())
}

func StacksRecord(class Class, slug string) RecordName {
	return append(ProjectRecord(class, slug), "stacks")
}

func EdgeStackRecord(class Class, slug string) RecordName {
	return rooted(rootEdgeStacks, class, slug)
}

func EdgeStacksRecord(class Class) RecordName {
	return rooted(rootEdgeStacks, class)
}

func WildcardRecord(class Class) RecordName { return rooted(rootWildcard, class) }

func LedgerRecord(scope string, rest ...string) RecordName {
	return append(RecordName{rootLedger, scope}, rest...)
}

var classSegment = map[string]int{
	rootSchema:     1,
	rootProjects:   1,
	rootBootstrap:  1,
	rootEdgeStacks: 1,
	rootWildcard:   1,
	rootLedger:     1,
	"conformance":  1,
	"valuerefs":    1,
	"values":       2,
}

func ClassOf(name RecordName) (Class, bool) {
	if len(name) == 0 {
		return "", false
	}
	at, named := classSegment[name[0]]
	if !named || len(name) <= at {
		return "", false
	}
	segment := name[at]
	if name[0] == rootLedger {
		segment, _, _ = strings.Cut(segment, naming.PathSeparator)
	}
	switch Class(segment) {
	case ClassProduction, ClassPreview:
		return Class(segment), true
	}
	return "", false
}

type Project struct {
	Features []string `json:"features,omitempty"`
}

type BootstrapState struct {
	AutoHeal bool `json:"auto_heal,omitempty"`
}
