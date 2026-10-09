package router

import (
	"context"
	"slices"
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

func Supports(r Router, need edge.Need) bool { return slices.Contains(r.Facts().Supported, need) }

type Kind string

type Propagation struct {
	Typical   time.Duration `json:"typical"`
	Published bool          `json:"published"`
}

type Facts struct {
	Propagation                 Propagation
	Supported                   []edge.Need
	CachesRecords               bool
	AddressesItself             bool
	SignsOriginForwards         bool
	ReachesFunctions            bool
	ReachesContainers           bool
	AnswersHostnames            bool
	StopsServingRemovedPointers bool
	ServesPreviewDeployments    bool
}

type Router interface {
	Kind() Kind

	Facts() Facts

	Hooks() Hooks

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (Stack, error)

	Open(state StackState) (Stack, error)
}

type Hooks struct {
	Origin      *OriginHooks
	RouteTables *RouteTableHooks
}

type RouteTableHooks struct {
	Store  func(ctx context.Context, state StackState, key string, table []byte) error
	Forget func(ctx context.Context, state StackState, key string) error
}

type OriginHooks struct {
	PlanProjectRemoval      func(scope edge.ProjectScope) []edge.PlanGroup
	ClaimPreviewEntry       func(ctx context.Context, claim Claim) (edge.Origin, error)
	DisclaimPreviewEntry    func(ctx context.Context, baseDomain string) error
	PlanPreviewEntryRemoval func(wildcard string) []edge.PlanGroup
}

type Claim struct {
	Hostname             string
	App                  string
	Pointer              string
	Certificate          string
	CertificateRequested bool
	ClientCAs            []string
	OriginCertificate    edge.OriginCertificate
	Tunnel               edge.Kind
}

type Stack interface {
	State() StackState

	Claim(ctx context.Context, claim Claim) (edge.Origin, error)

	Disclaim(ctx context.Context, hostname string) error

	MovePointer(ctx context.Context, move PointerMove, progress progress.Log) error

	RemovePointer(ctx context.Context, removal PointerRemoval, progress progress.Log) error

	Destroy(ctx context.Context) error
}
