package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Kind string

type ResourceKind string

const (
	ResourcePostgres ResourceKind = "postgres"
	ResourceBucket   ResourceKind = "bucket"
)

type Facts struct {
	Kind        Kind
	Serves      []ResourceKind
	Membrane    []ResourceKind
	Features    []string
	DefaultEdge edge.Kind
}

type Provider interface {
	Facts() Facts
	Substrate() Substrate
	Deployer() Deployer
	Records() RecordStore
	Vars() VarStore
	Edges() EdgeRegistry
	DNS() DNSRegistry
}

type Certifying interface {
	Certificates() Certificates
}

type EdgeRegistry interface {
	For(ctx context.Context, kind edge.Kind) (edge.Edge, error)
	Kinds() []edge.Kind
}

type DNSRegistry interface {
	WriterFor(ctx context.Context, kind, zone string) (edge.DNSWriter, error)
	Kinds() []string
}

type Progress interface {
	Stage(id StageID, title string, parent StageID)
	Start(id StageID)
	Done(id StageID)
	Fail(id StageID, err error)
	Say(id StageID, line string)
	Count(id StageID, current, total uint32)
}

type StageID string
