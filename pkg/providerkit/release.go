package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
)

// Releaser provisions one deploy's stacks and destroys them again. It is the
// only port that mutates app infrastructure, and the only one the Pulumi adapter
// implements — which is why Pulumi is opt-in composition and not a requirement.
//
// The kit hands it a plan whose stacks are already named, whose manifest is
// already validated and whose class is already resolved. The port turns that
// plan into vendor resources and reports what it made.
type Releaser interface {
	// Provision brings the plan up. It converges: the same plan applied twice is
	// the second one doing nothing.
	Provision(ctx context.Context, plan ReleasePlan, report Reporter) (ReleaseResult, error)

	// Destroy tears down exactly the stacks the scope names. The kit has already
	// built and shown the removal plan.
	Destroy(ctx context.Context, scope ReleaseScope, report Reporter) error

	// Sweep destroys stacks the records no longer account for. Prune's keep-N
	// policy is the kit's; the port is told which releases to sweep and sweeps
	// them.
	Sweep(ctx context.Context, scope SweepScope, report Reporter) error
}

// ReleasePlan is a whole deploy, resolved. Every name in it came from
// pkg/naming, so two vendors provisioning the same project agree on what things
// are called.
type ReleasePlan struct {
	Project string
	Class   Class
	Env     string
	Release naming.Release

	// Stacks is the unit of provisioning, in the order they must come up.
	Stacks []StackPlan
}

// StackPlan is one stack of a release.
//
// TODO(#507): the fidelity of Resources is the provisioning port's question, not
// this one. It is a placeholder here to prove the plan crosses the boundary as
// kit vocabulary rather than as a proto message.
type StackPlan struct {
	Name      naming.StackName
	Resources []Resource
}

// Resource is one thing the manifest asked for, already validated and with its
// tier mapped to a class.
type Resource struct {
	LogicalName string
	Type        string
	Linked      bool
	Config      map[string]any
}

// ReleaseResult is what the kit needs back to record the release, promote it and
// tell the user where it went. Values are opaque strings the kit stores and
// prints; it branches on none of them.
type ReleaseResult struct {
	// Origin is where the edge should point at this release.
	Origin string

	// Outputs is per-stack, keyed by the logical name the plan used.
	Outputs map[string]map[string]string
}

// ReleaseScope names stacks to destroy without describing them.
type ReleaseScope struct {
	Project string
	Class   Class
	Env     string
	Stacks  []naming.StackName
}

// SweepScope names releases the records no longer keep.
type SweepScope struct {
	Project string
	Class   Class
	Stale   []naming.StackName
}

// Warmer is optional: a vendor whose compute has a cold start may implement it,
// and the kit calls it after a release is promoted. A vendor that does not is not
// deficient, and conformance asserts nothing about it.
type Warmer interface {
	Warm(ctx context.Context, targets []string, report Reporter) error
}

// CodeEmbedder is optional: updating a deployed function's code in place, which
// is how a build's own artifacts reach a function that was provisioned before
// they existed. A vendor without in-place code update omits it, and the kit falls
// back to provisioning the artifact in.
type CodeEmbedder interface {
	EmbedCode(ctx context.Context, function string, artifact ArtifactRef, report Reporter) error
}
