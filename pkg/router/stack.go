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
