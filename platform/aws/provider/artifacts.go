package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func (p *Provider) Buckets(ctx context.Context, tier environment.Tier) (awsports.Buckets, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return awsports.Buckets{}, err
	}
	return awsports.Buckets{Artifacts: deployed.ArtifactBucket, Edge: deployed.EdgeBucket, Cache: deployed.CacheBucket}, nil
}
