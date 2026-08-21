// The names are the kit's, and a store maps them to whatever it has. The tree
// they form is versioned as a whole by the root schema record, which the
// Bootstrap handler writes after Bootstrapper.Apply: migrations are forward-only
// over the tree, so no record carries a version field of its own and no reader
// has to branch on one.

package providerkit

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

// SchemaRecord versions the whole tree beneath it.
func SchemaRecord() RecordName { return RecordName{"schema"} }

func ProjectRecord(slug string) RecordName { return RecordName{"projects", slug} }

func StackRecord(slug string, stack naming.StackName) RecordName {
	return RecordName{"projects", slug, "stacks", stack.String()}
}

func EdgeStackRecord(class Class, slug string) RecordName {
	return RecordName{"edgestacks", string(class), slug}
}

func WildcardRecord(class Class) RecordName { return RecordName{"wildcard", string(class)} }

// EdgePrivate is the subtree an edge keeps to itself — CloudFront's invalidation
// targets are the reason it exists. The kit builds the prefix and never reads
// beneath it.
func EdgePrivate(kind edge.Kind, rest ...string) RecordName {
	return append(RecordName{"edges", string(kind)}, rest...)
}

// LedgerRecord is the subtree providerkit/ledger owns, keyed by its scope. An
// edge with a ledger of its own writes nothing here.
func LedgerRecord(scope string, rest ...string) RecordName {
	return append(RecordName{"ledger", scope}, rest...)
}

// Project is the record at [ProjectRecord]. Features is what this project was
// bootstrapped expecting, so a deploy can refuse before it provisions rather
// than halfway through.
type Project struct {
	Features []string `json:"features,omitempty"`
}

// EdgeStackState is the record at [EdgeStackRecord]: the edge's own state as the
// contract spells it, plus the per-hostname settlement the kit drives and the
// edge has no opinion on.
type EdgeStackState struct {
	Edge  edge.StackState    `json:"edge"`
	Hosts map[string]Settled `json:"hosts,omitempty"`
}

// Settled is one hostname's progress through the settle loop, checkpointed at
// every step so an interrupted AddHostname resumes instead of restarting.
type Settled struct {
	Certificate string          `json:"certificate,omitempty"`
	Owed        json.RawMessage `json:"owed,omitempty"`
	Bound       bool            `json:"bound,omitempty"`
}
