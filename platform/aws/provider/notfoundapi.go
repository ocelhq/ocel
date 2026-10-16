package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/apigateway"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
)

func (p *Provider) notFoundAPIWork(tier environment.Tier) partWork {
	namespace := string(p.namespace)
	return partWork{
		name:    notfoundapi.Part,
		summary: notfoundapi.Summary,
		digest:  notfoundapi.Digest(namespace, tier),
		shape:   notfoundapi.Shape(namespace, tier),
		make: func(ctx context.Context, prior []provider.PartResource) ([]provider.PartResource, error) {
			return notfoundapi.Ensure(ctx, apigateway.NewFromConfig(p.aws), namespace, tier, prior)
		},
	}
}
