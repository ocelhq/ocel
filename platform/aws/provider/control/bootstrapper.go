package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const groupVendor providerkit.Vendor = "aws"

type SSMAPI interface {
	bootstrap.SSMAPI
}

type IAMAPI interface {
	bootstrap.IAMAPI
	bootstrap.IAMKeyAPI
}

type Bootstrapper struct {
	CFN     bootstrap.CFNAPI
	SSM     SSMAPI
	IAM     IAMAPI
	Store   bootstrap.ObjectStore
	Buckets bootstrap.BucketEmptierAPI
	Edge    edge.Edge
	Edges   providerkit.EdgeRegistry
}

func BootstrapperFor(cfg aws.Config, front edge.Edge, registry providerkit.EdgeRegistry) Bootstrapper {
	return Bootstrapper{
		CFN:     cloudformation.NewFromConfig(cfg),
		SSM:     ssm.NewFromConfig(cfg),
		IAM:     iam.NewFromConfig(cfg),
		Store:   s3.NewFromConfig(cfg),
		Buckets: s3.NewFromConfig(cfg),
		Edge:    front,
		Edges:   registry,
	}
}

func (b Bootstrapper) Catalogue() []providerkit.Feature { return bootstrap.Catalogue() }

func (b Bootstrapper) Describe(ctx context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	read, err := bootstrap.Read(ctx, b.CFN, string(class))
	if err != nil {
		return providerkit.Bootstrap{}, err
	}
	held := described(class, read.Deployed)
	held.Held = read
	return held, nil
}

func described(class providerkit.Class, deployed bootstrap.Deployed) providerkit.Bootstrap {
	described := providerkit.Bootstrap{Class: class, Present: deployed.Present}
	for _, stack := range deployed.Stacks {
		described.Stacks = append(described.Stacks, providerkit.BootstrapStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			Schema:        uint32(stack.Schema),
			DigestCurrent: stack.Current(),
			Writer:        stack.WrittenBy,
		})
	}
	return described
}

func (b Bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.Plan, error) {
	read, err := b.reading(ctx, req)
	if err != nil {
		return providerkit.Plan{}, err
	}
	groups, err := bootstrap.PlanChanges(ctx, b.CFN, read,
		bootstrap.Request{Features: req.Features, Remove: req.Remove, Writer: req.Writer},
		providerkit.DeriveGroups(bootstrap.NameStacks(described(req.Class, read.Deployed)), bootstrap.Catalogue(), req))
	if err != nil {
		return providerkit.Plan{}, err
	}
	adoptions, err := b.adoptions(ctx, req)
	if err != nil {
		return providerkit.Plan{}, err
	}
	params, err := bootstrap.PlanParameters(ctx,
		bootstrap.ParamAPIs{SSM: b.SSM, IAM: b.IAM},
		string(req.Class), adoptions,
		bootstrap.Request{Features: req.Features, Remove: req.Remove})
	if err != nil {
		return providerkit.Plan{}, err
	}
	plan := providerkit.Plan{Groups: providerkit.Vendored(groupVendor, append(groups, params))}
	fronts, err := b.edgeGroups(ctx, req)
	if err != nil {
		return providerkit.Plan{}, err
	}
	plan.Groups = append(plan.Groups, fronts...)
	return plan, nil
}

func (b Bootstrapper) reading(ctx context.Context, req providerkit.BootstrapRequest) (bootstrap.Reading, error) {
	if held, carried := req.Held.(bootstrap.Reading); carried && held.Class() == string(req.Class) {
		return held, nil
	}
	return bootstrap.Read(ctx, b.CFN, string(req.Class))
}

func (b Bootstrapper) open(kind edge.Kind) (edge.Edge, error) {
	if b.Edge != nil && b.Edge.Kind() == kind {
		return b.Edge, nil
	}
	return b.Edges.Open(kind)
}

func (b Bootstrapper) adoptions(ctx context.Context, req providerkit.BootstrapRequest) ([]bootstrap.EdgeAdoption, error) {
	var out []bootstrap.EdgeAdoption
	for _, kind := range bootstrap.EdgeKindsFor(req.Features) {
		front, err := b.open(kind)
		if err != nil {
			return nil, err
		}
		adopter, ok := front.(edge.BootstrapAdopter)
		if !ok {
			continue
		}
		adoption, err := adopter.Adoption(ctx, edge.Class(req.Class))
		if err != nil {
			return nil, fmt.Errorf("read what the %s edge hands this account to hold: %w", kind, err)
		}
		out = append(out, bootstrap.EdgeAdoption{Kind: kind, Adoption: adoption})
	}
	return out, nil
}

func (b Bootstrapper) edgeGroups(ctx context.Context, req providerkit.BootstrapRequest) ([]providerkit.ChangeGroup, error) {
	var groups []providerkit.ChangeGroup
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

func (b Bootstrapper) standingEdgeGroup(ctx context.Context, class providerkit.Class, kind edge.Kind) (*providerkit.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	planned, err := plannedBootstrap(ctx, front, class)
	if err != nil || len(planned) == 0 {
		return nil, err
	}
	group, err := providerkit.EdgeGroup(kind, providerkit.FeatureNeedingEdge(bootstrap.Catalogue(), kind), planned)
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func plannedBootstrap(ctx context.Context, front edge.Edge, class providerkit.Class) ([]edge.PlanChange, error) {
	planner, ok := front.(edge.BootstrapPlanner)
	if !ok {
		return nil, nil
	}
	planned, err := planner.PlanBootstrap(ctx, edge.Class(class))
	if err != nil {
		return nil, fmt.Errorf("plan the %s edge bootstrap: %w", front.Kind(), err)
	}
	return planned, nil
}

func (b Bootstrapper) severedEdge(ctx context.Context, class providerkit.Class, kind edge.Kind) (*providerkit.ChangeGroup, error) {
	front, err := b.open(kind)
	if err != nil {
		return nil, err
	}
	group, err := b.removedEdgeGroup(ctx, class, front)
	if err != nil {
		return nil, err
	}
	feature := providerkit.FeatureNeedingEdge(bootstrap.Catalogue(), kind)
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

func standingEdgeChanges(kind edge.Kind, feature string, planned []edge.PlanChange) *providerkit.ChangeGroup {
	group := providerkit.ChangeGroup{
		Kind:    providerkit.EdgeGroupKind,
		Name:    edge.EdgeGroupName(kind),
		Feature: feature,
		Action:  providerkit.ActionDelete,
	}
	for _, change := range planned {
		if change.Action == edge.PlanCreate {
			continue
		}
		group.Changes = append(group.Changes, providerkit.Change{
			Kind:   change.Kind,
			Name:   change.Name,
			Action: providerkit.ActionDelete,
		})
	}
	if len(group.Changes) == 0 {
		return nil
	}
	return &group
}

func (b Bootstrapper) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if req.Heal {
		return b.heal(ctx, req, report)
	}
	err := bootstrap.Run(ctx, b.apis(), string(req.Class), bootstrap.Request{
		Features:           req.Features,
		Remove:             req.Remove,
		Writer:             req.Writer,
		AcceptReplacements: !req.Unattended,
	}, say(report), detail(report))
	if bootstrap.RefusedWrite(err) {
		return providerkit.Refuse(providerkit.CodeDenied, "%s", err.Error())
	}
	return err
}

func (b Bootstrapper) heal(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	_, err := bootstrap.Heal(ctx, b.apis(), string(req.Class), bootstrap.HealRequest{
		Features: req.Features,
		Writer:   req.Writer,
	}, detail(report))
	if errors.Is(err, bootstrap.ErrHealNotPermitted) {
		return providerkit.Refuse(providerkit.CodeDenied, "%s", err.Error())
	}
	return err
}

func (b Bootstrapper) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	progress, logf := say(report), detail(report)
	read, err := bootstrap.Read(ctx, b.CFN, string(class))
	if err != nil {
		return err
	}
	fronts, err := b.standingEdges(ctx, class, read.Deployed)
	if err != nil {
		return err
	}
	for _, front := range fronts {
		progress(fmt.Sprintf("Tearing down the %s edge", front.Kind()))
		if err := front.Teardown(ctx, edge.Class(class)); err != nil {
			return fmt.Errorf("tear down %s edge: %w", front.Kind(), err)
		}
	}
	return bootstrap.Teardown(ctx, bootstrap.TeardownAPIs{
		CFN:     b.CFN,
		SSM:     b.SSM,
		IAM:     b.IAM,
		Buckets: b.Buckets,
	}, string(class), progress, logf)
}

func (b Bootstrapper) apis() bootstrap.APIs {
	return bootstrap.APIs{CFN: b.CFN, SSM: b.SSM, IAM: b.IAM, Store: b.Store, Edge: b.Edge, Edges: b.Edges}
}

func say(report providerkit.Reporter) func(string) {
	if report == nil {
		return func(string) {}
	}
	return report.Say
}

func detail(report providerkit.Reporter) func(string) {
	if report == nil {
		return func(string) {}
	}
	return report.Detail
}

var _ providerkit.Bootstrapper = Bootstrapper{}
