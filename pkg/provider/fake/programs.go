package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ProgramStore  = "fake-releases-store"
	ProgramSecret = "fake-origin-secret"
)

func (p *Provider) ProgramEdge(_ context.Context, req provider.EdgeProgramRequest) (edge.Program, error) {
	return edge.Program{
		Origin: edge.OriginReach{
			Reach:    edge.Reach{Kind: edge.ReachOriginSecret, Secret: ProgramSecret + "-" + string(req.Kind)},
			Throttle: edge.Throttle{Status: 429},
		},
		ReleasesStore: edge.ReleasesStore{Name: ProgramStore},
	}, nil
}
