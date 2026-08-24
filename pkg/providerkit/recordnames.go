package providerkit

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

func rooted(root string, class Class, rest ...string) RecordName {
	return append(RecordName{root, string(class)}, rest...)
}

func SchemaRecord(class Class) RecordName { return rooted(RootSchema, class) }

func ProjectsRecord(class Class) RecordName { return rooted(RootProjects, class) }

func ProjectRecord(class Class, slug string) RecordName {
	return rooted(RootProjects, class, slug)
}

func BootstrapRecord(class Class) RecordName { return rooted(RootBootstrap, class) }

func StackRecord(class Class, slug string, stack naming.StackName) RecordName {
	return append(StacksRecord(class, slug), stack.String())
}

func StacksRecord(class Class, slug string) RecordName {
	return append(ProjectRecord(class, slug), "stacks")
}

func EdgeStackRecord(class Class, slug string) RecordName {
	return rooted(RootEdgeStacks, class, slug)
}

func EdgeStacksRecord(class Class) RecordName {
	return rooted(RootEdgeStacks, class)
}

func WildcardRecord(class Class) RecordName { return rooted(RootWildcard, class) }

func LedgerRecord(scope string, rest ...string) RecordName {
	return append(RecordName{RootLedger, scope}, rest...)
}

var classSegment = map[string]int{
	RootSchema:      1,
	RootProjects:    1,
	RootBootstrap:   1,
	RootEdgeStacks:  1,
	RootWildcard:    1,
	RootLedger:      1,
	RootConformance: 1,
	RootValueRefs:   1,
	RootValues:      2,
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
	if name[0] == RootLedger {
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
