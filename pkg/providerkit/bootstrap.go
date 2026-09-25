package providerkit

import "context"

type Bootstrap interface {
	Catalogue() []Feature

	Describe(ctx context.Context, class Class) (BootstrapReading, error)

	Plan(ctx context.Context, req BootstrapRequest) (Plan, error)

	Apply(ctx context.Context, req BootstrapRequest, progress Progress) error

	PlanRemove(ctx context.Context, class Class) (Plan, error)

	Remove(ctx context.Context, class Class, progress Progress) error
}

type BootstrapReading struct {
	Class   Class
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
	Class Class

	Features []string

	Remove []string

	Unattended bool

	Heal bool

	WrittenBy WrittenBy

	Reading any
}
