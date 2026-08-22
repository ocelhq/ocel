package providerkit

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func SchemaRecord() RecordName { return RecordName{"schema"} }

func ProjectRecord(slug string) RecordName { return RecordName{"projects", slug} }

func StackRecord(slug string, stack naming.StackName) RecordName {
	return RecordName{"projects", slug, "stacks", stack.String()}
}

func StacksRecord(slug string) RecordName { return RecordName{"projects", slug, "stacks"} }

func EdgeStackRecord(class Class, slug string) RecordName {
	return RecordName{"edgestacks", string(class), slug}
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

type EdgeStackState struct {
	Edge  edge.StackState    `json:"edge"`
	Hosts map[string]Settled `json:"hosts,omitempty"`
}

type Settled struct {
	Certificate string          `json:"certificate,omitempty"`
	Owed        json.RawMessage `json:"owed,omitempty"`
	Bound       bool            `json:"bound,omitempty"`
}
