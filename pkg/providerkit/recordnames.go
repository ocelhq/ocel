package providerkit

import (
	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func SchemaRecord() RecordName { return RecordName{"schema"} }

func ProjectsRecord() RecordName { return RecordName{"projects"} }

func ProjectRecord(slug string) RecordName { return RecordName{"projects", slug} }

func BootstrapRecord(class Class) RecordName { return RecordName{"bootstrap", string(class)} }

func StackRecord(slug string, stack naming.StackName) RecordName {
	return RecordName{"projects", slug, "stacks", stack.String()}
}

func StacksRecord(slug string) RecordName { return RecordName{"projects", slug, "stacks"} }

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

type Project struct {
	Features []string `json:"features,omitempty"`
}

type BootstrapState struct {
	AutoHeal bool `json:"auto_heal,omitempty"`
}
