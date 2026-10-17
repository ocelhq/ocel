package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/cloudfrontpart"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

func (p *Provider) cloudFrontWork(tier environment.Tier) partWork {
	spec := cloudfrontpart.Spec{Namespace: p.namespace, Tier: tier}
	return partWork{
		name:    cloudfrontpart.Part,
		summary: cloudfrontpart.Summary,
		digest:  cloudfrontpart.Digest(spec),
		shape:   cloudfrontpart.Shape(p.namespace, tier),
		make: func(ctx context.Context, prior []provider.PartResource, _ bool) ([]provider.PartResource, error) {
			api, err := p.cloudFrontAPI(ctx)
			if err != nil {
				return nil, err
			}
			return cloudfrontpart.Ensure(ctx, api, spec, prior)
		},
	}
}

func (p *Provider) cloudFrontAPI(ctx context.Context) (cloudfrontpart.API, error) {
	global, err := sdkconfig.Control(ctx, certs.CloudFrontRegion)
	if err != nil {
		return nil, err
	}
	return cloudfront.NewFromConfig(global), nil
}
