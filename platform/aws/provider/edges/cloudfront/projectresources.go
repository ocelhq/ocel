package cloudfront

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func ReadProjectResources(ctx context.Context, store records.Store, ns account.Namespace, slug string, tier environment.Tier) ([]provider.PhysicalResource, error) {
	name, err := distributionName(ns, slug, tier)
	if err != nil {
		return nil, err
	}
	served, found, err := distributionRecords{keyValues: store, tier: tier}.find(ctx, name)
	if err != nil || !found {
		return nil, err
	}
	return []provider.PhysicalResource{cfn.NewPhysicalResource("AWS::CloudFront::Distribution", served.id, "")}, nil
}
