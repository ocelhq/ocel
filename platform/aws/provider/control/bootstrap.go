package control

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

const groupVendor provider.Vendor = "aws"

type SSMAPI interface {
	account.SSMAPI
}

type IAMAPI interface {
	account.IAMAPI
	account.IAMKeyAPI
	account.IAMBoundaryAPI
}

type LayerAPI interface {
	account.LayerAPI
	account.LayerTeardownAPI
}

type Bootstrap struct {
	CFN          cfn.API
	SSM          SSMAPI
	IAM          IAMAPI
	Layers       LayerAPI
	Records      account.RecordStore
	KMS          account.KeyAPI
	Store        account.ObjectStore
	Buckets      cfn.BucketEmptierAPI
	Edge         edge.Edge
	Edges        provider.Edges
	Kinds        []edge.Kind
	Region       string
	VariablesKey string
	Read         func(context.Context, environment.Tier) (account.Reading, error)
	TierParts    []tier.Part

	IdentityProviders account.IdentityProviderAPI

	Namespace account.Namespace
}

func BootstrapFor(cfg aws.Config, front edge.Edge, registry provider.Edges, kinds []edge.Kind, variablesKey string, ns account.Namespace, read func(context.Context, environment.Tier) (account.Reading, error)) Bootstrap {
	return Bootstrap{
		CFN:          cloudformation.NewFromConfig(cfg),
		SSM:          ssm.NewFromConfig(cfg),
		IAM:          iam.NewFromConfig(cfg),
		Layers:       lambda.NewFromConfig(cfg),
		Records:      account.DynamoRecords(dynamodb.NewFromConfig(cfg)),
		KMS:          kms.NewFromConfig(cfg),
		Store:        s3.NewFromConfig(cfg),
		Buckets:      s3.NewFromConfig(cfg),
		Edge:         front,
		Edges:        registry,
		Kinds:        kinds,
		Region:       cfg.Region,
		VariablesKey: variablesKey,
		Read:         read,

		IdentityProviders: iam.NewFromConfig(cfg),

		Namespace: ns,
	}
}

func (b Bootstrap) paramAPIs() account.ParamAPIs {
	return account.ParamAPIs{SSM: b.SSM, IAM: b.IAM, KMS: b.KMS, Region: b.Region, Stacks: b.CFN, IdentityProviders: b.IdentityProviders}
}

func (b Bootstrap) request(req provider.BootstrapRequest) (account.Request, error) {
	deploy, err := CredentialPolicies(b.Namespace, edge.PurposeDeploy, req.Tier, b.VariablesKey)
	if err != nil {
		return account.Request{}, err
	}
	return account.Request{
		VariablesKey:       b.VariablesKey,
		Parts:              req.Parts,
		Remove:             req.Remove,
		Writer:             req.WrittenBy,
		AcceptReplacements: !req.RefuseReplacements,
		Trust:              req.Trust,
		DeployPolicies:     deploy,
	}, nil
}

func (b Bootstrap) readAccount(ctx context.Context, tier environment.Tier) (account.Reading, error) {
	deploy, err := CredentialPolicies(b.Namespace, edge.PurposeDeploy, tier, b.VariablesKey)
	if err != nil {
		return account.Reading{}, err
	}
	return account.Read(ctx, b.CFN, b.Namespace, tier, deploy)
}

func (b Bootstrap) Parts() []provider.Part { return account.Parts() }

func (b Bootstrap) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	read, err := b.read(ctx, tier)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description := described(tier, read.Deployed)
	description.Identity, description.Trust = read.Deployed.Identity(), read.Deployed.Trust()
	edgeStacks, err := b.edgeBootstrapStacks(ctx, tier, read.Deployed)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description.Stacks = append(description.Stacks, edgeStacks...)
	return description, nil
}

func described(tier environment.Tier, deployed account.Deployed) provider.BootstrapDescription {
	described := provider.BootstrapDescription{Tier: tier, Present: deployed.Present}
	for _, stack := range deployed.Stacks {
		described.Stacks = append(described.Stacks, provider.BootstrapStack{
			Name:          stack.Name,
			Part:          stack.Part,
			Present:       stack.Present,
			DigestCurrent: stack.Current(),
			WrittenBy:     stack.WrittenBy,
		})
	}
	return described
}

func (b Bootstrap) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	read, err := b.read(ctx, req.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	planned, err := b.request(req)
	if err != nil {
		return provider.Plan{}, err
	}
	groups, err := account.PlanChanges(ctx, b.CFN, b.Layers, read, planned,
		bootstrapplan.ChangeGroups(account.WithDefaultStackNames(b.Namespace, described(req.Tier, read.Deployed)), account.Parts(), req))
	if err != nil {
		return provider.Plan{}, err
	}
	adoptions, err := b.adoptions(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	params, err := account.PlanParameters(ctx, b.paramAPIs(), b.Namespace, req.Tier, adoptions, planned)
	if err != nil {
		return provider.Plan{}, err
	}
	plan := provider.Plan{Groups: bootstrapplan.PrefixWithVendor(groupVendor, append(groups, params))}
	fronts, err := b.edgeGroups(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	plan.Groups = append(plan.Groups, fronts...)
	return plan, nil
}

func (b Bootstrap) read(ctx context.Context, tier environment.Tier) (account.Reading, error) {
	if b.Read == nil {
		return b.readAccount(ctx, tier)
	}
	return b.Read(ctx, tier)
}

func (b Bootstrap) open(kind edge.Kind) (edge.Edge, error) {
	if b.Edge != nil && b.Edge.Kind() == kind {
		return b.Edge, nil
	}
	return b.Edges.Open(kind, nil)
}

func (b Bootstrap) adoptions(ctx context.Context, req provider.BootstrapRequest) ([]account.EdgeAdoption, error) {
	var out []account.EdgeAdoption
	for _, kind := range account.EdgeKindsFor(req.Parts) {
		front, err := b.open(kind)
		if err != nil {
			return nil, err
		}
		adopt := front.Hooks().PlanAdoption
		if adopt == nil {
			continue
		}
		adoption, err := adopt(ctx, req.Tier)
		if err != nil {
			return nil, fmt.Errorf("read what the %s edge hands this account to store: %w", kind, err)
		}
		out = append(out, account.EdgeAdoption{Kind: kind, Adoption: adoption})
	}
	return out, nil
}

func (b Bootstrap) edgeGroups(ctx context.Context, req provider.BootstrapRequest) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, kind := range account.EdgeKindsFor(req.Parts) {
		group, err := b.installedEdgeGroup(ctx, req.Tier, kind)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	for _, kind := range account.EdgeKindsFor(account.Removing(req.Parts, req.Remove)) {
		group, err := b.severedEdge(ctx, req.Tier, kind)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (b Bootstrap) installedEdgeGroup(ctx context.Context, tier environment.Tier, kind edge.Kind) (*provider.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	return bootstrapplan.PlanEdgeBootstrap(ctx, front, tier, bootstrapplan.PartFrontingEdge(account.Parts(), kind))
}

func (b Bootstrap) severedEdge(ctx context.Context, tier environment.Tier, kind edge.Kind) (*provider.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	part := bootstrapplan.PartFrontingEdge(account.Parts(), kind)
	group, err := bootstrapplan.PlanEdgeRemoval(ctx, front, tier, part)
	if err != nil {
		return nil, err
	}
	if group == nil {
		installed, err := bootstrapplan.PlanEdgeBootstrap(ctx, front, tier, part)
		if err != nil {
			return nil, err
		}
		group = installedEdgeChanges(installed)
	}
	if group == nil {
		return nil, nil
	}
	group.Reason = fmt.Sprintf("dropping %s takes the %s edge with it", part, kind)
	return group, nil
}

func installedEdgeChanges(installed *provider.ChangeGroup) *provider.ChangeGroup {
	if installed == nil {
		return nil
	}
	group := provider.ChangeGroup{
		Kind:   installed.Kind,
		Name:   installed.Name,
		Part:   installed.Part,
		Action: provider.ActionDelete,
	}
	for _, change := range installed.Changes {
		if change.Action == provider.ActionCreate {
			continue
		}
		group.Changes = append(group.Changes, provider.Change{
			Kind:   change.Kind,
			Name:   change.Name,
			Action: provider.ActionDelete,
		})
	}
	if len(group.Changes) == 0 {
		return nil
	}
	return &group
}

func (b Bootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	applied, err := b.request(req)
	if err != nil {
		return err
	}
	err = account.Run(ctx, b.apis(), b.Namespace, req.Tier, applied, progress)
	if account.RefusedWrite(err) {
		return refusal.Refuse(refusal.CodeDenied, "%s", err.Error())
	}
	return err
}

func (b Bootstrap) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	read, err := b.readAccount(ctx, tier)
	if err != nil {
		return err
	}
	fronts, err := b.installedEdges(ctx, tier, read.Deployed)
	if err != nil {
		return err
	}
	for _, front := range fronts {
		if progress != nil {
			progress.Say(fmt.Sprintf("Tearing down the %s edge", front.Kind()))
		}
		if err := front.Teardown(ctx, tier); err != nil {
			return fmt.Errorf("tear down %s edge: %w", front.Kind(), err)
		}
	}
	return account.Teardown(ctx, account.TeardownAPIs{
		CFN:     b.CFN,
		SSM:     b.SSM,
		IAM:     b.IAM,
		Layers:  b.Layers,
		Records: b.Records,
		Buckets: b.Buckets,

		IdentityProviders: b.IdentityProviders,

		RemoveParts: b.tearDownParts,
	}, b.Namespace, tier, progress)
}

func (b Bootstrap) tearDownParts(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	for _, part := range b.TierParts {
		if part.Teardown == nil {
			continue
		}
		if err := part.Teardown.Remove(ctx, tier, progress); err != nil {
			return err
		}
	}
	return nil
}

func (b Bootstrap) apis() account.APIs {
	return account.APIs{CFN: b.CFN, SSM: b.SSM, IAM: b.IAM, Layers: b.Layers, Store: b.Store, Records: b.Records, Edge: b.Edge, Edges: b.Edges, IdentityProviders: b.IdentityProviders}
}

var _ provider.Bootstrap = Bootstrap{}
