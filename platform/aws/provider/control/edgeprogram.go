package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func refuseProgrammingEdge(context.Context, provider.EdgeProgramRequest) (edge.Program, error) {
	return edge.Program{}, provider.RefuseNeedsRunner("programming an edge")
}
