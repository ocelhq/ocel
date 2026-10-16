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
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
)

const groupVendor provider.Vendor = "aws"

type SSMAPI interface {
	bootstrap.SSMAPI
}

type IAMAPI interface {
	bootstrap.IAMAPI
	bootstrap.IAMKeyAPI
	bootstrap.IAMBoundaryAPI
}

type LayerAPI interface {
	bootstrap.LayerAPI
	bootstrap.LayerTeardownAPI
}

type Bootstrap struct {
	CFN          cfn.API
	SSM          SSMAPI
	IAM          IAMAPI
	Layers       LayerAPI
	Records      bootstrap.RecordStore
	KMS          bootstrap.KeyAPI
	Store        bootstrap.ObjectStore
	Buckets      cfn.BucketEmptierAPI
	Bastion      bastion.Clients
	EnvSync      envsourcesync.Clients
	Edge         edge.Edge
	Edges        provider.Edges
	Kinds        []edge.Kind
	Region       string
	VariablesKey string
	Read         func(context.Context, environment.Tier) (bootstrap.Reading, error)

	RemoveParts     func(ctx context.Context, tier environment.Tier) error
	PlanPartRemoval func(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error)

	Namespace bootstrap.Namespace
}

func BootstrapFor(cfg aws.Config, front edge.Edge, registry provider.Edges, kinds []edge.Kind, variablesKey string, ns bootstrap.Namespace, read func(context.Context, environment.Tier) (bootstrap.Reading, error)) Bootstrap {
	return Bootstrap{
		CFN:          cloudformation.NewFromConfig(cfg),
		SSM:          ssm.NewFromConfig(cfg),
		IAM:          iam.NewFromConfig(cfg),
		Layers:       lambda.NewFromConfig(cfg),
		Records:      bootstrap.DynamoRecords(dynamodb.NewFromConfig(cfg)),
		KMS:          kms.NewFromConfig(cfg),
		Store:        s3.NewFromConfig(cfg),
		Buckets:      s3.NewFromConfig(cfg),
		Bastion:      bastion.NewClients(cfg),
		EnvSync:      envsourcesync.NewClients(cfg),
		Edge:         front,
		Edges:        registry,
		Kinds:        kinds,
		Region:       cfg.Region,
		VariablesKey: variablesKey,
		Read:         read,

		Namespace: ns,
	}
}

func (b Bootstrap) paramAPIs() bootstrap.ParamAPIs {
	return bootstrap.ParamAPIs{SSM: b.SSM, IAM: b.IAM, KMS: b.KMS, Region: b.Region}
}

func (b Bootstrap) request(req provider.BootstrapRequest) bootstrap.Request {
	return bootstrap.Request{
		VariablesKey:       b.VariablesKey,
		Parts:              req.Parts,
		Remove:             req.Remove,
		Writer:             req.WrittenBy,
		AcceptReplacements: !req.RefuseReplacements,
	}
}

func (b Bootstrap) Parts() []provider.Part { return bootstrap.Parts() }

func (b Bootstrap) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	read, err := b.read(ctx, tier)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description := described(tier, read.Deployed)
	edgeStacks, err := b.edgeBootstrapStacks(ctx, tier, read.Deployed)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description.Stacks = append(description.Stacks, edgeStacks...)
	description.VendorState = read
	return description, nil
}

func described(tier environment.Tier, deployed bootstrap.Deployed) provider.BootstrapDescription {
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
	read, err := b.reading(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	groups, err := bootstrap.PlanChanges(ctx, b.CFN, b.Layers, read, b.request(req),
		bootstrapplan.ChangeGroups(bootstrap.WithDefaultStackNames(b.Namespace, described(req.Tier, read.Deployed)), bootstrap.Parts(), req))
	if err != nil {
		return provider.Plan{}, err
	}
	adoptions, err := b.adoptions(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	params, err := bootstrap.PlanParameters(ctx, b.paramAPIs(), b.Namespace, req.Tier, adoptions, b.request(req))
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

func (b Bootstrap) read(ctx context.Context, tier environment.Tier) (bootstrap.Reading, error) {
	if b.Read == nil {
		return bootstrap.Read(ctx, b.CFN, b.Namespace, tier)
	}
	return b.Read(ctx, tier)
}

func (b Bootstrap) reading(ctx context.Context, req provider.BootstrapRequest) (bootstrap.Reading, error) {
	if prior, passed := req.VendorState.(bootstrap.Reading); passed && prior.Tier() == req.Tier {
		return prior, nil
	}
	return bootstrap.Read(ctx, b.CFN, b.Namespace, req.Tier)
}

func (b Bootstrap) open(kind edge.Kind) (edge.Edge, error) {
	if b.Edge != nil && b.Edge.Kind() == kind {
		return b.Edge, nil
	}
	return b.Edges.Open(kind, nil)
}

func (b Bootstrap) adoptions(ctx context.Context, req provider.BootstrapRequest) ([]bootstrap.EdgeAdoption, error) {
	var out []bootstrap.EdgeAdoption
	for _, kind := range bootstrap.EdgeKindsFor(req.Parts) {
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
		out = append(out, bootstrap.EdgeAdoption{Kind: kind, Adoption: adoption})
	}
	return out, nil
}

func (b Bootstrap) edgeGroups(ctx context.Context, req provider.BootstrapRequest) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, kind := range bootstrap.EdgeKindsFor(req.Parts) {
		group, err := b.installedEdgeGroup(ctx, req.Tier, kind)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	for _, kind := range bootstrap.EdgeKindsFor(bootstrap.Removing(req.Parts, req.Remove)) {
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
	planned, err := plannedBootstrap(ctx, front, tier)
	if err != nil || len(planned) == 0 {
		return nil, err
	}
	group, err := bootstrapplan.EdgeGroup(kind, bootstrapplan.PartFrontingEdge(bootstrap.Parts(), kind), planned)
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func plannedBootstrap(ctx context.Context, front edge.Edge, tier environment.Tier) ([]edge.PlanChange, error) {
	plan := front.Hooks().PlanBootstrap
	if plan == nil {
		return nil, nil
	}
	planned, err := plan(ctx, tier)
	if err != nil {
		return nil, fmt.Errorf("plan the %s edge bootstrap: %w", front.Kind(), err)
	}
	return planned, nil
}

func (b Bootstrap) severedEdge(ctx context.Context, tier environment.Tier, kind edge.Kind) (*provider.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	group, err := b.removedEdgeGroup(ctx, tier, front)
	if err != nil {
		return nil, err
	}
	part := bootstrapplan.PartFrontingEdge(bootstrap.Parts(), kind)
	if group == nil {
		planned, err := plannedBootstrap(ctx, front, tier)
		if err != nil {
			return nil, err
		}
		group = installedEdgeChanges(kind, part, planned)
	}
	if group == nil {
		return nil, nil
	}
	group.Reason = fmt.Sprintf("dropping %s takes the %s edge with it", part, kind)
	return group, nil
}

func installedEdgeChanges(kind edge.Kind, part string, planned []edge.PlanChange) *provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(kind),
		Part:   part,
		Action: provider.ActionDelete,
	}
	for _, change := range planned {
		if change.Action == edge.PlanCreate {
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
	err := bootstrap.Run(ctx, b.apis(), b.Namespace, req.Tier, b.request(req), progress)
	if bootstrap.RefusedWrite(err) {
		return refusal.Refuse(refusal.CodeDenied, "%s", err.Error())
	}
	return err
}

func (b Bootstrap) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	read, err := bootstrap.Read(ctx, b.CFN, b.Namespace, tier)
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
	return bootstrap.Teardown(ctx, bootstrap.TeardownAPIs{
		CFN:     b.CFN,
		SSM:     b.SSM,
		IAM:     b.IAM,
		Layers:  b.Layers,
		Records: b.Records,
		Buckets: b.Buckets,

		RemoveBastion:       b.removeBastion,
		RemoveEnvSourceSync: b.removeEnvSourceSync,
		RemoveParts:         b.RemoveParts,
	}, b.Namespace, tier, progress)
}

func (b Bootstrap) removeEnvSourceSync(ctx context.Context, tier environment.Tier) error {
	return envsourcesync.Remove(ctx, b.EnvSync, string(b.Namespace), tier)
}

func (b Bootstrap) removeBastion(ctx context.Context, tier environment.Tier) error {
	return bastion.Remove(ctx, b.Bastion, tier)
}

func (b Bootstrap) apis() bootstrap.APIs {
	return bootstrap.APIs{CFN: b.CFN, SSM: b.SSM, IAM: b.IAM, Layers: b.Layers, Store: b.Store, Records: b.Records, Edge: b.Edge, Edges: b.Edges}
}

var _ provider.Bootstrap = Bootstrap{}
