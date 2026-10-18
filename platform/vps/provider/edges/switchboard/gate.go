package switchboard

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Purge func(ctx context.Context, hostnames []string) error

func (e *Edge) publishGate(ctx context.Context, publication edge.GatePublication) error {
	return e.putGate(ctx, publication.State.Slug, publication.State.Tier, publication.Gate)
}

func (e *Edge) putGate(ctx context.Context, slug string, tier environment.Tier, held *gate.Gate) error {
	if err := e.routes.PutGate(ctx, Surface(slug, tier), held); err != nil {
		return fmt.Errorf("hand the switchboard the %s gate of %s: %w", tier, slug, err)
	}
	return nil
}

func (s *stack) putRecordedGate(ctx context.Context) error {
	held, err := s.e.readGate(ctx, s.state.Tier, s.state.Slug)
	if err != nil {
		return fmt.Errorf("read the %s gate of %s: %w", s.state.Tier, s.state.Slug, err)
	}
	return s.e.putGate(ctx, s.state.Slug, s.state.Tier, held)
}

func (e *Edge) ReconcileGateBehind(ctx context.Context, spec edge.StackSpec, prior edge.StackState, purge Purge) error {
	if spec.Slug == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"the box serves a project by its slug, and this stack names none")
	}
	return e.putGateBehind(ctx, spec.Slug, spec.Tier, spec.Gate, prior.Bound, purge)
}

func (e *Edge) PublishGateBehind(purge Purge) func(context.Context, edge.GatePublication) error {
	return func(ctx context.Context, publication edge.GatePublication) error {
		state := publication.State
		return e.putGateBehind(ctx, state.Slug, state.Tier, publication.Gate, state.Bound, purge)
	}
}

func (e *Edge) putGateBehind(ctx context.Context, slug string, tier environment.Tier, held *gate.Gate, bound []string, purge Purge) error {
	if err := e.putGate(ctx, slug, tier, held); err != nil {
		return err
	}
	if held == nil || len(bound) == 0 {
		return nil
	}
	if purge == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s gate of %s is on the box, and the edge in front of it cannot purge what it cached of %v before the gate landed", tier, slug, bound)
	}
	if err := purge(ctx, bound); err != nil {
		return fmt.Errorf("the %s gate of %s is on the box, and the edge in front of it may still answer %v from what it cached before: %w", tier, slug, bound, err)
	}
	return nil
}
