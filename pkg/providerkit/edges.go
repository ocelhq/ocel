package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Edges interface {
	Open(kind edge.Kind) (edge.Edge, error)
}
