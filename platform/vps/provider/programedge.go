package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	boxThrottleStatus            = 429
	imageEndpointMaxRequestBytes = 32 << 20
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (edge.Program, error) {
	held, err := p.holdFronting(ctx, req.Tier, naming.Sanitize(p.project))
	if err != nil {
		return edge.Program{}, err
	}
	return edge.Program{
		Origin: edge.OriginReach{
			Reach:    edge.Reach{Kind: edge.ReachFronted},
			Throttle: edge.Throttle{Status: boxThrottleStatus},
			Images:   &edge.OriginImages{MaxRequestBytes: imageEndpointMaxRequestBytes},
		},
		ReleasesStore: held.store,
	}, nil
}
