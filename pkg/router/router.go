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

type FlipBound struct {
	Typical   time.Duration `json:"typical"`
	Published bool          `json:"published"`
}

type Facts struct {
	FlipBound                   FlipBound
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

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (Stack, error)

	Open(state StackState) (Stack, error)
}

type Claim struct {
	Hostname          string
	App               string
	Certificate       string
	ClientCertificate string
}

type Stack interface {
	State() StackState

	Claim(ctx context.Context, claim Claim) (edge.Origin, error)

	Disclaim(ctx context.Context, hostname string) error

	Flip(ctx context.Context, flip Flip, progress progress.Progress) error

	RemovePointer(ctx context.Context, pointer string, progress progress.Progress) error

	Destroy(ctx context.Context) error
}
