package aws

import (
	"context"
	"fmt"
	"slices"

	apigatewaysdk "github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func (p *Provider) partRecords() tier.Records {
	return tier.Records{KeyValues: p.KeyValues(), WrittenBy: p.writtenBy, Region: p.aws.Region, Reads: &p.partReads}
}

func (p *Provider) tierParts(records tier.Records) tier.Parts {
	namespace := string(p.namespace)
	region := p.aws.Region
	return tier.Parts{
		control.RuntimeLayersPart(lambda.NewFromConfig(p.aws), account.DynamoRecords(dynamodb.NewFromConfig(p.aws)), p.namespace, region, p.readBootstrap),
		bastion.NewPart(bastion.NewClients(p.aws), namespace, region, p.releaseBastionNetwork),
		containers.NewPart(p.stacks.Containers(), records.KeyValues, namespace, region),
		functionnetwork.NewPart(p.stacks.FunctionNetwork(), records.KeyValues, region),
		network.NewPart(p.stacks.Network(), records.KeyValues, region),
		envsourcesync.NewPart(records, envsourcesync.NewClients(p.aws), namespace, p.envSourceSyncSpec),
		notfoundapi.NewPart(records, apigatewaysdk.NewFromConfig(p.aws), namespace, apigateway.Kind),
		cloudfrontpart.NewPart(records, p.namespace, cloudfront.Kind, cloudfrontpart.OpenAPI),
		imageoptimizer.NewPart(records, imageoptimizer.NewClients(p.aws), namespace, p.imageOptimizerSpec),
		taginvalidator.NewPart(records, taginvalidator.NewClients(p.aws), namespace, p.tagInvalidatorSpec),
	}
}

func (p *Provider) imageOptimizerSpec(ctx context.Context, tier environment.Tier) (imageoptimizer.Spec, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return imageoptimizer.Spec{}, err
	}
	if err := p.requireBootstrapped(deployed, tier); err != nil {
		return imageoptimizer.Spec{}, err
	}
	return imageoptimizer.Spec{
		Namespace:     string(p.namespace),
		Tier:          tier,
		Region:        p.aws.Region,
		Boundary:      deployed.AppBoundaryARN,
		RolePath:      p.namespace.AppRolePathFor(tier),
		EdgeBucket:    deployed.Buckets[naming.StorageEdge],
		EdgeBucketARN: "arn:aws:s3:::" + deployed.Buckets[naming.StorageEdge],
	}, nil
}

func (p *Provider) tagInvalidatorSpec(ctx context.Context, tier environment.Tier) (taginvalidator.Spec, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return taginvalidator.Spec{}, err
	}
	if err := p.requireBootstrapped(deployed, tier); err != nil {
		return taginvalidator.Spec{}, err
	}
	accountID, err := p.accountID(ctx)
	if err != nil {
		return taginvalidator.Spec{}, err
	}
	seed, err := account.PurgeSeedParamFor(p.namespace, tier, account.KindCloudflare)
	if err != nil {
		return taginvalidator.Spec{}, err
	}
	return taginvalidator.Spec{
		Namespace:  string(p.namespace),
		Tier:       tier,
		Region:     p.aws.Region,
		Account:    accountID,
		Role:       deployed.TagInvalidatorRoleARN,
		StateTable: deployed.StateTable,
		StreamARN:  deployed.StateTableStreamARN,
		SeedParam:  seed,
	}, nil
}

func (p *Provider) ensurePartNamed(ctx context.Context, tier environment.Tier, name, addedBy, addedWith string, say func(string)) ([]provider.PhysicalResource, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, refusal.Refuse(refusal.CodeNotReady, "the %s tier is not bootstrapped, so there is nowhere to record its %s part: run `%s`", tier, name, provider.BootstrapCommand(tier))
	}
	part, found := p.tierParts(p.partRecords()).Named(name)
	if !found || part.Added == nil {
		return nil, fmt.Errorf("this provider adds no bootstrap part named %q", name)
	}
	return part.Added.Ensure(ctx, tier, addedBy, addedWith, say)
}

func (p *Provider) planDeployParts(ctx context.Context, pre provider.DeployPreflight) ([]provider.ChangeGroup, error) {
	tier := pre.Deploy.Tier
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil || !deployed.Present {
		return nil, err
	}
	parts := p.tierParts(p.partRecords())
	used := parts.AddedFor(pre.Uses, pre.Edge)
	var groups []provider.ChangeGroup
	for _, part := range used {
		group, err := part.Added.Plan(ctx, tier)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (p *Provider) planNetworkRetiredWith(ctx context.Context, at environment.Tier, removed []string) (*provider.ChangeGroup, error) {
	return planNetworkRetiredWith(ctx, p.KeyValues(), p.aws.Region, p.stacks.Network().PartsByConsumer, at, removed)
}

func planNetworkRetiredWith(ctx context.Context, store records.Store, region string, partsByConsumer map[string]string, at environment.Tier, removed []string) (*provider.ChangeGroup, error) {
	if slices.Contains(removed, network.Part) {
		return nil, nil
	}
	part, present, err := network.ReadPart(ctx, store, region, at)
	if err != nil || !present || len(part.UsedBy) == 0 {
		return nil, err
	}
	for _, user := range part.UsedBy {
		if leaving, isPart := partsByConsumer[user]; !isPart || !slices.Contains(removed, leaving) {
			return nil, nil
		}
	}
	return tier.Deletion(network.Part, "the last part that runs in it is removed by this run, and the network goes with it", part.Resources), nil
}

func (p *Provider) releaseBastionNetwork(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	return p.stacks.Network().Release(ctx, p.KeyValues(), bastion.ConsumerRef(tier), progress)
}
