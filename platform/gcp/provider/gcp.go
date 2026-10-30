package gcp

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/connector"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

type Provider struct {
	*control.Provider

	gateInEnv bool

	writtenBy  provider.WrittenBy
	transforms transform.Pass

	evaluatedFronts     sync.Map
	evaluatedNetworks   sync.Map
	evaluatedPlacements sync.Map
	appEdges            alb.AppPatches

	surveyed bootstrap.Surveys

	images *run.Images
}

func New(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
	base, err := control.New(ctx, settings)
	if err != nil {
		return nil, err
	}
	p := assembled(base)
	p.writtenBy = settings.WrittenBy
	p.transforms = transformPass(settings.ProjectDir, settings.Transforms)
	return p, nil
}

func NewProvider(options control.Options) (*Provider, error) {
	base, err := control.NewProvider(options)
	if err != nil {
		return nil, err
	}
	return assembled(base), nil
}

func assembled(base *control.Provider) *Provider {
	p := newProvider(base)
	p.gateInEnv = base.Emulated()
	p.images = &run.Images{Pull: run.PullBase}
	return p
}

func newProvider(base *control.Provider) *Provider {
	p := &Provider{Provider: base}
	base.Engine = engine{p: p}
	return p
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		Preflight:            &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight},
		InfraPreflight:       &provider.PreflightHooks{Plan: p.PlanInfraPreflight, Apply: p.ApplyInfraPreflight},
		ProgramEdge:          p.ProgramEdge,
		EnsureImageRegistry:  p.EnsureImageRegistry,
		OpenDirectImages:     p.OpenDirectImages,
		CheckBucket:          s3store.Check,
		EnvSource:            &provider.EnvSourceHooks{ProveIdentity: ports.ProveIdentity, EnsureSync: p.EnsureEnvSourceSync},
		BindingAccess:        &provider.BindingAccessHooks{ForwardPorts: p.ForwardPorts, ServeProxy: p.ServeBindingProxy},
		Cost:                 &provider.CostHooks{Inventory: p.InventoryDeploy, Estimate: control.EstimateCost},
		Retirement:           &provider.RetirementHooks{Plan: p.PlanRetirement, Retire: p.Retire},
		ProjectRemoval:       &provider.ProjectRemovalHooks{Plan: p.PlanProjectRemoval, Remove: p.RemoveProject},
		ListProjectResources: p.ListProjectResources,
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	if _, err := p.Edges().Open(kind, nil); err != nil {
		return nil, err
	}
	return bootstrapGate{p: p}, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return stacks{Stacks: resources.NewHookStacks(p.KeyValues(), p.resourceHooks()), p: p}
}

func (p *Provider) Edges() provider.Edges { return p.edges() }

func (p *Provider) Routers() provider.Routers { return edges.Routers{Registry: p.edges()} }

func (p *Provider) Connector() provider.Connector {
	return connector.New(p.OpenClients, p.Services(), p.Options.Region, p.Emulated(), p.images.Based, p.pushImage,
		func(ctx context.Context, tier environment.Tier) error { return p.ensureKeys(ctx, tier, "") })
}

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }
