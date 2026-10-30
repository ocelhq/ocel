package alb

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

type AssetsBuckets struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
}

const loadBalancerAgentDomain = "@https-lb.iam.gserviceaccount.com"

func (p *AssetsBuckets) Read(ctx context.Context, tier environment.Tier) (albresources.AssetsBucket, error) {
	clients, err := p.OpenClients(ctx)
	if err != nil {
		return albresources.AssetsBucket{}, err
	}
	buckets, err := clients.TierBuckets(ctx, tier, naming.StorageEdge)
	if err != nil {
		return albresources.AssetsBucket{}, err
	}
	agent, err := clients.ReadServiceAgent(ctx, loadBalancerAgentDomain)
	if err != nil {
		return albresources.AssetsBucket{}, err
	}
	return albresources.AssetsBucket{Name: buckets[naming.StorageEdge], Agent: agent}, nil
}
