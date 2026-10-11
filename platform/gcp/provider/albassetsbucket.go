package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const loadBalancerAgentDomain = "@https-lb.iam.gserviceaccount.com"

func (p *Provider) readAssetsBucket(ctx context.Context, tier environment.Tier) (alb.AssetsBucket, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return alb.AssetsBucket{}, err
	}
	agent, err := clients.ReadServiceAgent(ctx, loadBalancerAgentDomain)
	if err != nil {
		return alb.AssetsBucket{}, err
	}
	return alb.AssetsBucket{Name: clients.Bucket(tier), Agent: agent}, nil
}
