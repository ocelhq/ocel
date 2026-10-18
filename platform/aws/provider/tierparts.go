package aws

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/vpcclients"

	apigatewaysdk "github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
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
		bastion.NewPart(bastion.NewClients(p.aws), region),
		vpcclients.NewPart(records, ec2.NewFromConfig(p.aws), namespace),
		containers.NewPart(p.stacks.Containers(), records.KeyValues, region),
		functionnetwork.NewPart(p.stacks.FunctionNetwork(), records.KeyValues, region),
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
		Namespace:      string(p.namespace),
		Tier:           tier,
		Region:         p.aws.Region,
		Boundary:       deployed.AppBoundaryARN,
		RolePath:       p.namespace.AppRolePathFor(tier),
		AssetBucket:    deployed.AssetBucket,
		AssetBucketARN: "arn:aws:s3:::" + deployed.AssetBucket,
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

func (p *Provider) ensurePartNamed(ctx context.Context, tier environment.Tier, name, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
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

func (p *Provider) planDeployParts(ctx context.Context, spec provider.StackSpec) ([]provider.ChangeGroup, error) {
	tier := spec.Ref.Tier
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil || !deployed.Present {
		return nil, err
	}
	if spec.App == nil {
		return p.planVPCClients(ctx, tier, spec.Resources)
	}
	var uses []buildoutput.Uses
	if spec.App.Compute != provider.ComputeContainer {
		used, err := usesOf(spec.App.OutputRoot, spec.App.App, spec.App.Framework)
		if err != nil {
			return nil, err
		}
		uses = []buildoutput.Uses{used}
	}
	front := edge.None
	if spec.Edge != nil {
		front = spec.Edge.Kind()
	}
	var groups []provider.ChangeGroup
	for _, part := range p.tierParts(p.partRecords()).AddedFor(uses, front) {
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

func usesOf(root, app, framework string) (buildoutput.Uses, error) {
	if root == "" {
		return buildoutput.PresumeUses(framework), nil
	}
	hosting, present, err := buildoutput.ReadHosting(root, app)
	if err != nil || !present {
		return buildoutput.PresumeUses(framework), err
	}
	_, revalidates := hosting.Needs[edge.NeedRevalidation]
	_, err = os.Stat(filepath.Join(buildoutput.AppRoot(root, app), naming.ImageConfigFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return buildoutput.Uses{}, fmt.Errorf("read the image config of %s: %w", app, err)
	}
	return buildoutput.Uses{Revalidation: revalidates, ImageOptimization: err == nil}, nil
}

func (p *Provider) planVPCClients(ctx context.Context, tier environment.Tier, resources []provider.Resource) ([]provider.ChangeGroup, error) {
	if len(awsresources.Registry(awsresources.Clients{}).PlacesInVPC(resources)) == 0 {
		return nil, nil
	}
	part, found := p.tierParts(p.partRecords()).Named(vpcclients.Part)
	if !found || part.Added == nil {
		return nil, fmt.Errorf("this provider adds no bootstrap part named %q", vpcclients.Part)
	}
	group, err := part.Added.Plan(ctx, tier)
	if err != nil || group == nil {
		return nil, err
	}
	return []provider.ChangeGroup{*group}, nil
}

func (p *Provider) vpcClients(ctx context.Context, tier environment.Tier) (string, error) {
	recorded, present, err := p.partRecords().Read(ctx, tier, vpcclients.Part)
	if err != nil || !present {
		return "", err
	}
	return vpcclients.GroupOf(recorded.Resources), nil
}
