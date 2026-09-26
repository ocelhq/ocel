package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/liveness"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) servedOnTheBox(ctx context.Context, hostname string) (edge.Kind, error) {
	said, err := p.host.ServedEdge(ctx, hostname)
	if err != nil {
		return "", err
	}
	if said.Unreached != "" {
		return "", liveness.ProbeUnanswered{Cause: said.Unreached}
	}
	return edge.Kind(said.Edge), nil
}
