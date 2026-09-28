package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
)

type Edges interface {
	Open(kind edge.Kind) (edge.Edge, error)
}
