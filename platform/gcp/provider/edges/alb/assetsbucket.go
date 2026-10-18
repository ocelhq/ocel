package alb

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

type AssetsBuckets struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
}

const loadBalancerAgentDomain = "@https-lb.iam.gserviceaccount.com"

func (p *AssetsBuckets) Read(ctx context.Context, tier environment.Tier) (AssetsBucket, error) {
	clients, err := p.OpenClients(ctx)
	if err != nil {
		return AssetsBucket{}, err
	}
	agent, err := clients.ReadServiceAgent(ctx, loadBalancerAgentDomain)
	if err != nil {
		return AssetsBucket{}, err
	}
	return AssetsBucket{Name: clients.Bucket(tier), Agent: agent}, nil
}
