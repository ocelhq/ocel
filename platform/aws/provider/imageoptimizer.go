package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
)

func (p *Provider) imageOptimizerWork(ctx context.Context, tier environment.Tier) (partWork, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return partWork{}, err
	}
	if err := p.requireBootstrapped(deployed, tier); err != nil {
		return partWork{}, err
	}
	spec := imageoptimizer.Spec{
		Namespace:      string(p.namespace),
		Tier:           tier,
		Region:         p.aws.Region,
		Boundary:       deployed.AppBoundaryARN,
		RolePath:       p.namespace.AppRolePathFor(tier),
		AssetBucket:    deployed.AssetBucket,
		AssetBucketARN: "arn:aws:s3:::" + deployed.AssetBucket,
	}
	return partWork{
		name:    imageoptimizer.Part,
		summary: imageoptimizer.Summary,
		digest:  imageoptimizer.Digest(spec),
		shape:   imageoptimizer.Shape(spec.Namespace, tier),
		make: func(ctx context.Context, _ []provider.PartResource, updatesTrust bool) ([]provider.PartResource, error) {
			spec := spec
			spec.UpdatesTrust = updatesTrust
			return imageoptimizer.Ensure(ctx, imageoptimizer.NewClients(p.aws), spec)
		},
	}, nil
}

func (p *Provider) imageOptimizerURL(ctx context.Context, tier environment.Tier) (string, error) {
	recorded, present, err := p.readPart(ctx, tier, imageoptimizer.Part)
	if err != nil || !present || recorded.State != stackrecords.PartReady {
		return "", err
	}
	return imageoptimizer.URLOf(recorded.Resources), nil
}
