package control

import (
	"context"
	"fmt"

	apigatewaysdk "github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func (p *Provider) PartRecords() tier.Records {
	return tier.Records{KeyValues: p.KeyValues(), WrittenBy: p.WrittenBy, Region: p.Config.Region, Reads: &p.partReads}
}

type EngineParts struct {
	Bastion        tier.Part
	SharedStacks   tier.Parts
	ImageOptimizer tier.Part
	TagInvalidator tier.Part
}

func (p *Provider) TierParts(records tier.Records, engine EngineParts) tier.Parts {
	namespace := string(p.Namespace)
	region := p.Config.Region
	parts := tier.Parts{
		RuntimeLayersPart(lambda.NewFromConfig(p.Config), account.DynamoRecords(dynamodb.NewFromConfig(p.Config)), p.Namespace, region, p.readBootstrap),
		engine.Bastion,
	}
	parts = append(parts, engine.SharedStacks...)
	parts = append(parts, envsourcesync.NewPart(records, envsourcesync.NewClients(p.Config), namespace, p.envSourceSyncSpec))
	parts = append(parts, p.edgeParts(records)...)
	return append(parts, engine.ImageOptimizer, engine.TagInvalidator)
}

func (p *Provider) describedEngineParts(records tier.Records) EngineParts {
	namespace := string(p.Namespace)
	region := p.Config.Region
	return EngineParts{
		Bastion: removedByRunner(bastion.DescribedPart(bastion.NewClients(p.Config), namespace, region)),
		SharedStacks: tier.Parts{
			describedByRunner(sharedparts.ContainersPart, sharedparts.ContainersSummary, func(tier environment.Tier) []pricing.Resource {
				return sharedparts.ContainersInventory(tier, sharedparts.RaisedFronts{})
			}, func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
				return sharedparts.ReadContainersPart(ctx, records.KeyValues, namespace, region, tier)
			}),
			describedByRunner(sharedparts.FunctionNetworkPart, sharedparts.FunctionNetworkSummary, sharedparts.FunctionNetworkInventory, func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
				return sharedparts.ReadFunctionNetworkPart(ctx, records.KeyValues, region, tier)
			}),
			describedByRunner(sharedparts.NetworkPart, sharedparts.NetworkSummary, sharedparts.NetworkInventory, func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
				return sharedparts.ReadNetworkPart(ctx, records.KeyValues, region, tier)
			}),
		},
		ImageOptimizer: removedByRunner(imageoptimizer.DescribedPart(records, namespace, p.ImageOptimizerSpec)),
		TagInvalidator: removedByRunner(taginvalidator.DescribedPart(records, namespace, p.TagInvalidatorSpec)),
	}
}

func describedByRunner(name, summary string, inventory func(environment.Tier) []pricing.Resource, describe func(context.Context, environment.Tier) (provider.BootstrapPart, bool, error)) tier.Part {
	return removedByRunner(tier.Part{Name: name, Summary: summary, Inventory: inventory, Describe: describe})
}

func removedByRunner(part tier.Part) tier.Part {
	refuse := func() error { return provider.RefuseNeedsRunner("removing the " + part.Name + " part") }
	part.Removal = &tier.Removal{
		Plan:   func(context.Context, environment.Tier) (*provider.ChangeGroup, error) { return nil, refuse() },
		Remove: func(context.Context, environment.Tier, progress.Log) error { return refuse() },
	}
	return part
}

func (p *Provider) edgeParts(records tier.Records) tier.Parts {
	return tier.Parts{
		notfoundapi.NewPart(records, notfoundapi.NewAPIGatewayCalls(apigatewaysdk.NewFromConfig(p.Config)), string(p.Namespace), apigateway.Kind),
		cloudfrontpart.NewPart(records, p.Namespace, cloudfront.Kind, cloudfrontpart.OpenAPI),
	}
}

func (p *Provider) ImageOptimizerSpec(ctx context.Context, tier environment.Tier) (imageoptimizer.Spec, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return imageoptimizer.Spec{}, err
	}
	if err := deployed.RefuseUnbootstrapped(tier); err != nil {
		return imageoptimizer.Spec{}, err
	}
	return imageoptimizer.Spec{
		Namespace:     string(p.Namespace),
		Tier:          tier,
		Region:        p.Config.Region,
		Boundary:      deployed.AppBoundaryARN,
		RolePath:      p.Namespace.AppRolePathFor(tier),
		EdgeBucket:    deployed.Buckets[naming.StorageEdge],
		EdgeBucketARN: "arn:aws:s3:::" + deployed.Buckets[naming.StorageEdge],
	}, nil
}

func (p *Provider) TagInvalidatorSpec(ctx context.Context, tier environment.Tier) (taginvalidator.Spec, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return taginvalidator.Spec{}, err
	}
	if err := deployed.RefuseUnbootstrapped(tier); err != nil {
		return taginvalidator.Spec{}, err
	}
	accountID, err := p.ReadAccountID(ctx)
	if err != nil {
		return taginvalidator.Spec{}, err
	}
	seed, err := account.PurgeSeedParamFor(p.Namespace, tier, account.KindCloudflare)
	if err != nil {
		return taginvalidator.Spec{}, err
	}
	return taginvalidator.Spec{
		Namespace:  string(p.Namespace),
		Tier:       tier,
		Region:     p.Config.Region,
		Account:    accountID,
		Role:       deployed.TagInvalidatorRoleARN,
		StateTable: deployed.StateTable,
		StreamARN:  deployed.StateTableStreamARN,
		SeedParam:  seed,
	}, nil
}

func (p *Provider) ensurePartNamed(ctx context.Context, tier environment.Tier, name, addedBy, addedWith string, say func(string)) ([]provider.PhysicalResource, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, refusal.Refuse(refusal.CodeNotReady, "the %s tier is not bootstrapped, so there is nowhere to record its %s part: run `%s`", tier, name, provider.BootstrapCommand(tier))
	}
	part, found := p.edgeParts(p.PartRecords()).Named(name)
	if !found || part.Added == nil {
		return nil, fmt.Errorf("this provider adds no bootstrap part named %q", name)
	}
	return part.Added.Ensure(ctx, tier, addedBy, addedWith, say)
}
