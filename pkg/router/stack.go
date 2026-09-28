package router

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type StackSpec struct {
	Tier environment.Tier
	Slug string
}

type StackState struct {
	Slug string           `json:"slug,omitempty"`
	Tier environment.Tier `json:"tier,omitempty"`
	Edge edge.StackState  `json:"edge,omitzero"`
}

func NewStackState(shared edge.StackState) StackState {
	return StackState{Slug: shared.Slug, Tier: shared.Tier, Edge: shared}
}

func (s StackState) WithSpec(spec StackSpec) StackState {
	shared := s.Edge
	shared.Slug, shared.Tier = spec.Slug, spec.Tier
	return NewStackState(shared)
}
