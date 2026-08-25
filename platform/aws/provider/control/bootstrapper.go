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
}

func BootstrapperFor(cfg aws.Config, front edge.Edge) Bootstrapper {
	return Bootstrapper{
		CFN:     cloudformation.NewFromConfig(cfg),
		SSM:     ssm.NewFromConfig(cfg),
		IAM:     iam.NewFromConfig(cfg),
		Store:   s3.NewFromConfig(cfg),
		Buckets: s3.NewFromConfig(cfg),
		Edge:    front,
	}
}

func (b Bootstrapper) Catalogue() []providerkit.Feature { return bootstrap.Catalogue() }

func (b Bootstrapper) Describe(ctx context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	deployed, err := bootstrap.CheckDeployedFor(ctx, b.CFN, string(class))
	if err != nil {
		return providerkit.Bootstrap{}, err
	}
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
	return described, nil
}

func (b Bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.BootstrapPlan, error) {
	described, err := b.Describe(ctx, req.Class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	groups := providerkit.DeriveGroups(bootstrap.NameStacks(described), bootstrap.Catalogue(), req)
	for i, group := range groups {
		if group.Action == providerkit.ActionUpdate {
			groups[i].Reason = providerkit.WithoutDetail(group.Reason)
		}
	}
	return providerkit.BootstrapPlan{Groups: groups}, nil
}

func (b Bootstrapper) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if req.Heal {
		return b.heal(ctx, req, report)
	}
	err := bootstrap.Run(ctx, b.apis(), string(req.Class), bootstrap.Request{
		Features:           req.Features,
		Drop:               req.Drop,
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

func (b Bootstrapper) Removals(ctx context.Context, class providerkit.Class) ([]providerkit.Removal, error) {
	deployed, err := bootstrap.CheckDeployedFor(ctx, b.CFN, string(class))
	if err != nil {
		return nil, err
	}
	shared, err := bootstrap.PassphraseHeldBySibling(ctx, b.CFN, string(class))
	if err != nil {
		return nil, err
	}
	return removals(string(class), b.Edge.Kind(), deployed, shared)
}

func (b Bootstrapper) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	progress, logf := say(report), detail(report)
	progress(fmt.Sprintf("Tearing down the %s edge", b.Edge.Kind()))
	if err := b.Edge.Teardown(ctx, edge.Class(class)); err != nil {
		return fmt.Errorf("tear down %s edge: %w", b.Edge.Kind(), err)
	}
	return bootstrap.Teardown(ctx, bootstrap.TeardownAPIs{
		CFN:     b.CFN,
		SSM:     b.SSM,
		IAM:     b.IAM,
		Buckets: b.Buckets,
	}, string(class), progress, logf)
}

func (b Bootstrapper) apis() bootstrap.APIs {
	return bootstrap.APIs{CFN: b.CFN, SSM: b.SSM, IAM: b.IAM, Store: b.Store, Edge: b.Edge}
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
