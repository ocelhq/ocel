package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Edges interface {
	Supported() []edge.Kind

	Default() edge.Kind

	Open(kind edge.Kind) (edge.Edge, error)
}
