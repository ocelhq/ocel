package cloudfront

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
)

type edgeSet struct {
	keyValueStoreARN     string
	functionARN          string
	emptyBodyFunctionARN string
	cachePolicy          string
	headersPolicy        string
	originAccessControl  string
}

func edgeSetFrom(set cloudfrontpart.Set) edgeSet {
	return edgeSet{
		keyValueStoreARN:     set.RoutesStore,
		functionARN:          set.Resolver,
		emptyBodyFunctionARN: set.EmptyBody,
		cachePolicy:          set.CachePolicy,
		headersPolicy:        set.HeadersPolicy,
		originAccessControl:  set.AssetAccess,
	}
}

func partRecords(c Clients, deployed account.Deployed) records.Store {
	return awsports.KeyValues{Dynamo: c.Dynamo, Tables: awsports.Table(deployed.StateTable)}
}

func (p *cloudFront) readEdgeSet(ctx context.Context, c Clients, deployed account.Deployed, tier environment.Tier) (edgeSet, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, partRecords(c, deployed), tier, cloudfrontpart.Part)
	if err != nil {
		return edgeSet{}, err
	}
	set := cloudfrontpart.SetOf(cloudfrontpart.NamesFor(p.ns, tier), recorded.Resources)
	if !present || recorded.State != stackrecords.PartReady || !set.Complete() {
		return edgeSet{}, refuseUnbootstrapped(tier)
	}
	return edgeSetFrom(set), nil
}

func (p *cloudFront) ensureEdgeSet(ctx context.Context, tier environment.Tier, addedBy, addedWith string, say func(string)) (edgeSet, error) {
	if p.ensurePart == nil {
		return edgeSet{}, fmt.Errorf("the %q edge was built without a way to add the %s bootstrap part", Kind, cloudfrontpart.Part)
	}
	resources, err := p.ensurePart(ctx, tier, cloudfrontpart.Part, addedBy, addedWith, say)
	if err != nil {
		return edgeSet{}, err
	}
	set := cloudfrontpart.SetOf(cloudfrontpart.NamesFor(p.ns, tier), resources)
	if !set.Complete() {
		return edgeSet{}, refuseUnbootstrapped(tier)
	}
	return edgeSetFrom(set), nil
}

func refuseUnbootstrapped(tier environment.Tier) error {
	return fmt.Errorf("the %s tier in this account has nothing the %q edge fronts deployments with: its resolver function, key value store and cache policies make up the %s bootstrap part, which no deploy has added. Deploy a project through this edge to add it", tier, Kind, cloudfrontpart.Part)
}
