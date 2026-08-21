package providerkit

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

// Bootstrapper stands up the per-account, per-class foundation everything else
// is deployed onto, and reports on it. On AWS that is CloudFormation stacks,
// parameters and an index table; on a bare host it could be a directory and a
// unit file. The kit never learns which.
//
// The port reports facts. Whether a bootstrap is stale, whether this build would
// downgrade it, whether a feature the request needs is missing, and what to tell
// the user about any of that, are kit rules over those facts.
type Bootstrapper interface {
	// Describe answers for one class without changing anything. A class that was
	// never bootstrapped is not an error: it is a Bootstrap with Present false.
	Describe(ctx context.Context, class Class) (Bootstrap, error)

	// Apply makes the bootstrap match the request. It is the one write the kit
	// calls repeatedly and expects to converge: standing up, adding a feature and
	// healing a stale stack are the same call with different facts on either side.
	Apply(ctx context.Context, req BootstrapRequest, report Reporter) error

	// Removals lists what removing this class would touch, so the kit can build a
	// removal plan and show it before anything is destroyed. It changes nothing.
	Removals(ctx context.Context, class Class) ([]Removal, error)

	// Remove tears down what Apply stood up. The kit has already refused if the
	// class is occupied; the port does not re-litigate that.
	Remove(ctx context.Context, class Class, report Reporter) error
}

// Bootstrap is the state of one class, as facts.
type Bootstrap struct {
	Class   Class
	Present bool

	// Writer identifies the build that last wrote this bootstrap. The kit
	// compares it against its own and decides whether that is a downgrade.
	Writer string

	Stacks []BootstrapStack
}

// BootstrapStack is one unit of the foundation — one CloudFormation stack today.
// The kit uses Schema and DigestCurrent to decide staleness, and Feature to
// decide whether a request's needs are met.
type BootstrapStack struct {
	Name    string
	Feature string
	Present bool

	// Schema is the shape the stack was written against. The vendor owns the
	// number; the kit owns the comparison.
	Schema uint32

	// DigestCurrent says whether the stack's content matches what this build
	// carries. Only the vendor can know this — it hashes its own templates.
	DigestCurrent bool

	Required bool
}

// BootstrapRequest is what the kit asks for, already validated: the class is
// real, the features are known, and nothing here needs checking again.
type BootstrapRequest struct {
	Class Class

	// Features names the optional capabilities this bootstrap should carry. An
	// empty slice means the required set only.
	Features []string

	// Edge is the edge this account will front its apps with, because on some
	// vendors the bootstrap must mint credentials the edge will use.
	Edge edge.Kind
}

// Removal is one thing a teardown would touch. It is the edge contract's surface
// vocabulary rather than a second one, so a removal plan reads the same whether
// the item came from the origin or the edge.
type Removal = edge.Surface
