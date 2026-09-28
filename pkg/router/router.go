package router

import (
	"context"
	"time"

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
	FlipBound             FlipBound
	CachesRecords         bool
	RoutesPreviewsByLabel bool
	AddressesItself       bool
	SignsOriginForwards   bool
	ReachesFunctions      bool
	ReachesContainers     bool
	Dispatches            bool
	AnswersHostnames      bool
}

type Router interface {
	Kind() Kind

	Facts() Facts

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (Stack, error)

	Open(state StackState) (Stack, error)
}

type Stack interface {
	State() StackState

	Claim(ctx context.Context, hostname, app string) error

	Disclaim(ctx context.Context, hostname string) error

	Flip(ctx context.Context, flip Flip, progress progress.Progress) error

	RemovePointer(ctx context.Context, pointer string, progress progress.Progress) error

	Destroy(ctx context.Context) error
}
