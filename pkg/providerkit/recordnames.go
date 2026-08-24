package providerkit

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func SchemaRecord(class Class) RecordName { return RecordName{"schema", string(class)} }

func ProjectsRecord(class Class) RecordName { return RecordName{"projects", string(class)} }

func ProjectRecord(class Class, slug string) RecordName {
	return append(ProjectsRecord(class), slug)
}

func BootstrapRecord(class Class) RecordName { return RecordName{"bootstrap", string(class)} }

func StackRecord(class Class, slug string, stack naming.StackName) RecordName {
	return append(StacksRecord(class, slug), stack.String())
}

func StacksRecord(class Class, slug string) RecordName {
	return append(ProjectRecord(class, slug), "stacks")
}

func EdgeStackRecord(class Class, slug string) RecordName {
	return append(EdgeStacksRecord(class), slug)
}

func EdgeStacksRecord(class Class) RecordName {
	return RecordName{"edgestacks", string(class)}
}

func WildcardRecord(class Class) RecordName { return RecordName{"wildcard", string(class)} }

func EdgePrivate(kind edge.Kind, rest ...string) RecordName {
	return append(RecordName{"edges", string(kind)}, rest...)
}

func LedgerRecord(scope string, rest ...string) RecordName {
	return append(RecordName{"ledger", scope}, rest...)
}

var classSegment = map[string]int{
	"schema":      1,
	"projects":    1,
	"bootstrap":   1,
	"conformance": 1,
	"edgestacks":  1,
	"wildcard":    1,
	"valuerefs":   1,
	"ledger":      1,
	"values":      2,
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
	if name[0] == "ledger" {
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
