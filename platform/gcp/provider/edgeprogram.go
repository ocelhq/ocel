package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (edge.Program, error) {
	return p.offers().Program(ctx, req)
}
