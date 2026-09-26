package providerkit

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Bootstrap interface {
	Catalogue() []Feature

	Describe(ctx context.Context, class edge.Class) (BootstrapReading, error)

	Plan(ctx context.Context, req BootstrapRequest) (Plan, error)

	Apply(ctx context.Context, req BootstrapRequest, progress edge.Progress) error

	PlanRemove(ctx context.Context, class edge.Class) (Plan, error)

	Remove(ctx context.Context, class edge.Class, progress edge.Progress) error
}

type BootstrapReading struct {
	Class   edge.Class
	Present bool
	Stacks  []BootstrapStack

	Unfinished bool

	Reading any
}

type BootstrapStack struct {
	Name    string
	Feature string
	Present bool

	Schema uint32

	DigestCurrent bool

	WrittenBy string
}

type Feature struct {
	Name      string
	Summary   string
	DependsOn []string
	Needs     []string
}

const (
	NeedsFrameworkPrefix = "framework:"
	NeedsEdgePrefix      = "edge:"
)

type BootstrapRequest struct {
	Class edge.Class

	Features []string

	Remove []string

	Unattended bool

	Heal bool

	WrittenBy WrittenBy

	Reading any
}
