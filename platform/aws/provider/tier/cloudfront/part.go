package cloudfront

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(records tier.Records, ns account.Namespace, front edge.Kind, open func(context.Context) (API, error)) tier.Part {
	return tier.Recorded(records, tier.RecordedSpec{
		Name:    Part,
		Summary: Summary,
		Shape:   func(tier environment.Tier) []pricing.Shaped { return Shape(ns, tier) },
		Work: func(_ context.Context, tier environment.Tier) (tier.Work, error) {
			return Work(open, Spec{Namespace: ns, Tier: tier}), nil
		},
		Remove: func(ctx context.Context, tier environment.Tier, recorded []provider.PartResource) error {
			api, err := open(ctx)
			if err != nil {
				return err
			}
			return Remove(ctx, api, Spec{Namespace: ns, Tier: tier}, recorded)
		},
		IsUsedBy: func(_ []buildoutput.Uses, used edge.Kind) bool { return used == front },
		Wildcard: front,
	})
}

func Work(open func(context.Context) (API, error), spec Spec) tier.Work {
	return tier.Work{
		Digest: Digest(spec),
		Make: func(ctx context.Context, prior []provider.PartResource, _ bool) ([]provider.PartResource, error) {
			api, err := open(ctx)
			if err != nil {
				return nil, err
			}
			return Ensure(ctx, api, spec, prior)
		},
	}
}

func OpenAPI(ctx context.Context) (API, error) {
	global, err := sdkconfig.Control(ctx, certs.CloudFrontRegion)
	if err != nil {
		return nil, err
	}
	return cloudfront.NewFromConfig(global), nil
}
