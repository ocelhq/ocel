package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

type Provider struct {
	*control.Provider

	transforms []string
	projectDir string
	pullImage  images.ImagePull

	stacks *deploy.Stacks
}

func New(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
	assembled, err := control.New(ctx, settings)
	if err != nil {
		return nil, err
	}
	p := withEngine(assembled, settings.Transforms)
	p.projectDir = settings.ProjectDir
	return p, nil
}

func NewProvider(options control.Options, transforms []string, cfg aws.Config, ns account.Namespace) *Provider {
	return withEngine(control.NewProvider(options, cfg, ns), transforms)
}

func withEngine(assembled *control.Provider, transforms []string) *Provider {
	p := &Provider{Provider: assembled, transforms: transforms}
	p.stacks = deploy.NewStacks(p.release, &deploy.Realized{})
	p.stacks.Statements = rdsdata.NewFromConfig(assembled.Config)
	return p
}

func (p *Provider) Hooks() provider.Hooks {
	hooks := p.Provider.Hooks()
	hooks.Preflight = &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight}
	hooks.ProgramEdge = p.ProgramEdge
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
	hooks.OpenRegistryImages = p.OpenRegistryImages
	hooks.BindingAccess = &provider.BindingAccessHooks{ForwardPorts: p.ForwardPorts, ServeProxy: hooks.BindingAccess.ServeProxy}
	hooks.Cost = &provider.CostHooks{Inventory: p.InventoryDeploy, Estimate: hooks.Cost.Estimate}
	hooks.Retirement = &provider.RetirementHooks{Plan: p.PlanRetirement, Retire: p.Retire}
	return hooks
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	return control.TierBootstrap(p.Provider, kind, p.tierParts(p.PartRecords()), p.planNetworkRetiredWith)
}

func (p *Provider) Stacks() provider.Stacks { return p.stacks }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }
