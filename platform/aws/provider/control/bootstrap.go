package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

type Bootstrap struct {
	CFN     cfn.API
	SSM     SSMAPI
	IAM     IAMAPI
	KMS     bootstrap.KeyAPI
	Store   bootstrap.ObjectStore
	Buckets cfn.BucketEmptierAPI
	Edge    edge.Edge
	Edges   provider.Edges
	Kinds   []edge.Kind
	Region  string
	VarsKey string

	Namespace bootstrap.Namespace
}

func BootstrapFor(cfg aws.Config, front edge.Edge, registry provider.Edges, kinds []edge.Kind, varsKey string, ns bootstrap.Namespace) Bootstrap {
	return Bootstrap{
		CFN:     cloudformation.NewFromConfig(cfg),
		SSM:     ssm.NewFromConfig(cfg),
		IAM:     iam.NewFromConfig(cfg),
		KMS:     kms.NewFromConfig(cfg),
		Store:   s3.NewFromConfig(cfg),
		Buckets: s3.NewFromConfig(cfg),
		Edge:    front,
		Edges:   registry,
		Kinds:   kinds,
		Region:  cfg.Region,
		VarsKey: varsKey,

		Namespace: ns,
	}
}

func (b Bootstrap) paramAPIs() bootstrap.ParamAPIs {
	return bootstrap.ParamAPIs{SSM: b.SSM, IAM: b.IAM, KMS: b.KMS, Region: b.Region}
}

func (b Bootstrap) request(req provider.BootstrapRequest) bootstrap.Request {
	return bootstrap.Request{
		VarsKey:            b.VarsKey,
		Features:           req.Features,
		Remove:             req.Remove,
		Writer:             req.WrittenBy,
		AcceptReplacements: !req.RefuseReplacements,
	}
}

func (b Bootstrap) Catalogue() []provider.Feature { return bootstrap.Catalogue() }

func (b Bootstrap) Describe(ctx context.Context, class edge.Class) (provider.BootstrapDescription, error) {
	read, err := bootstrap.Read(ctx, b.CFN, b.Namespace, string(class))
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	held := described(class, read.Deployed)
	held.VendorState = read
	return held, nil
}

func described(class edge.Class, deployed bootstrap.Deployed) provider.BootstrapDescription {
	described := provider.BootstrapDescription{Class: class, Present: deployed.Present}
	for _, stack := range deployed.Stacks {
		described.Stacks = append(described.Stacks, provider.BootstrapStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			Schema:        uint32(stack.Schema),
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
	groups, err := bootstrap.PlanChanges(ctx, b.CFN, read, b.request(req),
		bootstrapplan.ChangeGroups(bootstrap.NameStacks(b.Namespace, described(req.Class, read.Deployed)), bootstrap.Catalogue(), req))
	if err != nil {
		return provider.Plan{}, err
	}
	adoptions, err := b.adoptions(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	params, err := bootstrap.PlanParameters(ctx, b.paramAPIs(), b.Namespace, string(req.Class), adoptions, b.request(req))
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

func (b Bootstrap) reading(ctx context.Context, req provider.BootstrapRequest) (bootstrap.Reading, error) {
	if held, carried := req.VendorState.(bootstrap.Reading); carried && held.Class() == string(req.Class) {
		return held, nil
	}
	return bootstrap.Read(ctx, b.CFN, b.Namespace, string(req.Class))
}

func (b Bootstrap) open(kind edge.Kind) (edge.Edge, error) {
	if b.Edge != nil && b.Edge.Kind() == kind {
		return b.Edge, nil
	}
	return b.Edges.Open(kind)
}

func (b Bootstrap) adoptions(ctx context.Context, req provider.BootstrapRequest) ([]bootstrap.EdgeAdoption, error) {
	var out []bootstrap.EdgeAdoption
	for _, kind := range bootstrap.EdgeKindsFor(req.Features) {
		front, err := b.open(kind)
		if err != nil {
			return nil, err
		}
		adopt := front.Hooks().PlanAdoption
		if adopt == nil {
			continue
		}
		adoption, err := adopt(ctx, req.Class)
		if err != nil {
			return nil, fmt.Errorf("read what the %s edge hands this account to hold: %w", kind, err)
		}
		out = append(out, bootstrap.EdgeAdoption{Kind: kind, Adoption: adoption})
	}
	return out, nil
}

func (b Bootstrap) edgeGroups(ctx context.Context, req provider.BootstrapRequest) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, kind := range bootstrap.EdgeKindsFor(req.Features) {
		group, err := b.standingEdgeGroup(ctx, req.Class, kind)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	for _, kind := range bootstrap.EdgeKindsFor(bootstrap.Removing(req.Features, req.Remove)) {
		group, err := b.severedEdge(ctx, req.Class, kind)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (b Bootstrap) standingEdgeGroup(ctx context.Context, class edge.Class, kind edge.Kind) (*provider.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	planned, err := plannedBootstrap(ctx, front, class)
	if err != nil || len(planned) == 0 {
		return nil, err
	}
	group, err := bootstrapplan.EdgeGroup(kind, bootstrapplan.FeatureNeedingEdge(bootstrap.Catalogue(), kind), planned)
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func plannedBootstrap(ctx context.Context, front edge.Edge, class edge.Class) ([]edge.PlanChange, error) {
	plan := front.Hooks().PlanBootstrap
	if plan == nil {
		return nil, nil
	}
	planned, err := plan(ctx, class)
	if err != nil {
		return nil, fmt.Errorf("plan the %s edge bootstrap: %w", front.Kind(), err)
	}
	return planned, nil
}

func (b Bootstrap) severedEdge(ctx context.Context, class edge.Class, kind edge.Kind) (*provider.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	group, err := b.removedEdgeGroup(ctx, class, front)
	if err != nil {
		return nil, err
	}
	feature := bootstrapplan.FeatureNeedingEdge(bootstrap.Catalogue(), kind)
	if group == nil {
		planned, err := plannedBootstrap(ctx, front, class)
		if err != nil {
			return nil, err
		}
		group = standingEdgeChanges(kind, feature, planned)
	}
	if group == nil {
		return nil, nil
	}
	group.Reason = fmt.Sprintf("dropping %s takes the %s edge with it", feature, kind)
	return group, nil
}

func standingEdgeChanges(kind edge.Kind, feature string, planned []edge.PlanChange) *provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:    provider.EdgeGroupKind,
		Name:    edge.EdgeGroupName(kind),
		Feature: feature,
		Action:  provider.ActionDelete,
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

func (b Bootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	if req.Heal {
		return b.heal(ctx, req, progress)
	}
	err := bootstrap.Run(ctx, b.apis(), b.Namespace, string(req.Class), b.request(req), say(progress), detail(progress))
	if bootstrap.RefusedWrite(err) {
		return refusal.Refuse(refusal.CodeDenied, "%s", err.Error())
	}
	return err
}

func (b Bootstrap) heal(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	_, err := bootstrap.Heal(ctx, b.apis(), b.Namespace, string(req.Class), bootstrap.HealRequest{
		Features: req.Features,
		Writer:   req.WrittenBy,
	}, detail(progress))
	if errors.Is(err, bootstrap.ErrHealNotPermitted) {
		return refusal.Refuse(refusal.CodeDenied, "%s", err.Error())
	}
	return err
}

func (b Bootstrap) Remove(ctx context.Context, class edge.Class, progress edge.Progress) error {
	sayf, logf := say(progress), detail(progress)
	read, err := bootstrap.Read(ctx, b.CFN, b.Namespace, string(class))
	if err != nil {
		return err
	}
	fronts, err := b.standingEdges(ctx, class, read.Deployed)
	if err != nil {
		return err
	}
	for _, front := range fronts {
		sayf(fmt.Sprintf("Tearing down the %s edge", front.Kind()))
		if err := front.Teardown(ctx, class); err != nil {
			return fmt.Errorf("tear down %s edge: %w", front.Kind(), err)
		}
	}
	return bootstrap.Teardown(ctx, bootstrap.TeardownAPIs{
		CFN:     b.CFN,
		SSM:     b.SSM,
		IAM:     b.IAM,
		Buckets: b.Buckets,
	}, b.Namespace, string(class), sayf, logf)
}

func (b Bootstrap) apis() bootstrap.APIs {
	return bootstrap.APIs{CFN: b.CFN, SSM: b.SSM, IAM: b.IAM, Store: b.Store, Edge: b.Edge, Edges: b.Edges}
}

func say(progress edge.Progress) func(string) {
	if progress == nil {
		return func(string) {}
	}
	return progress.Say
}

func detail(progress edge.Progress) func(string) {
	if progress == nil {
		return func(string) {}
	}
	return progress.Detail
}

var _ provider.Bootstrap = Bootstrap{}
