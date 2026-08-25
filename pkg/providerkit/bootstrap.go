package providerkit

import "context"

type Bootstrapper interface {
	Catalogue() []Feature

	Describe(ctx context.Context, class Class) (Bootstrap, error)

	Plan(ctx context.Context, req BootstrapRequest) (BootstrapPlan, error)

	Apply(ctx context.Context, req BootstrapRequest, report Reporter) error

	PlanRemoval(ctx context.Context, class Class) (BootstrapPlan, error)

	Remove(ctx context.Context, class Class, report Reporter) error
}

type Bootstrap struct {
	Class   Class
	Present bool
	Stacks  []BootstrapStack
}

type BootstrapStack struct {
	Name    string
	Feature string
	Present bool

	Schema uint32

	DigestCurrent bool

	Writer string
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

	Writer Writer
}
