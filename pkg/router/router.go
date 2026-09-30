package router

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
)

const DefaultPointer = "@production"

func ResolvePointer(pointer string) string {
	if pointer == "" {
		return DefaultPointer
	}
	return pointer
}

func IsDefaultPointer(pointer string) bool { return ResolvePointer(pointer) == DefaultPointer }

type Kind string

type Propagation struct {
	Typical   time.Duration `json:"typical"`
	Published bool          `json:"published"`
}

type Facts struct {
	Propagation                 Propagation
	CachesRecords               bool
	RoutesPreviewsByLabel       bool
	AddressesItself             bool
	SignsOriginForwards         bool
	ReachesFunctions            bool
	ReachesContainers           bool
	Dispatches                  bool
	AnswersHostnames            bool
	StopsServingRemovedPointers bool
}

type Router interface {
	Kind() Kind

	Facts() Facts

	Hooks() Hooks

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (Stack, error)

	Open(state StackState) (Stack, error)
}

type Hooks struct {
	Origin *OriginHooks
}

type OriginHooks struct {
	PlanProjectRemoval      func(scope edge.ProjectScope) []edge.PlanGroup
	ClaimPreviewEntry       func(ctx context.Context, claim Claim) (edge.Origin, error)
	DisclaimPreviewEntry    func(ctx context.Context, baseDomain string) error
	PlanPreviewEntryRemoval func(wildcard string) []edge.PlanGroup
}

type Claim struct {
	Hostname           string
	App                string
	Certificate        string
	ClientCertificates []string
	OriginCertificate  edge.OriginCertificate
	Tunnel             edge.Kind
}

type Stack interface {
	State() StackState

	Claim(ctx context.Context, claim Claim) (edge.Origin, error)

	Disclaim(ctx context.Context, hostname string) error

	MovePointer(ctx context.Context, move PointerMove, progress progress.Log) error

	RemovePointer(ctx context.Context, pointer string, progress progress.Log) error

	Destroy(ctx context.Context) error
}
