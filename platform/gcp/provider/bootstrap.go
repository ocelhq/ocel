package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

type bootstrapGate struct{ p *Provider }

func (g bootstrapGate) Parts() []provider.Part {
	return bootstrap.Engine{Removable: tierParts(network.Patches{})}.Parts()
}

func (g bootstrapGate) openBootstrap(ctx context.Context) (bootstrap.Engine, error) {
	opened, err := g.p.openClients(ctx)
	if err != nil {
		return bootstrap.Engine{}, err
	}
	stores, err := opened.Memorystore()
	if err != nil {
		return bootstrap.Engine{}, err
	}
	return bootstrap.Engine{
		Clients:        opened,
		Edges:          g.p.Edges(),
		Stores:         stores,
		Records:        g.p.KeyValues(),
		WrittenBy:      g.p.writtenBy,
		Surveyed:       &g.p.surveyed,
		SkipChecks:     g.p.skipChecks,
		Removable:      tierParts(network.Patches{}),
		Accounts:       tierAccounts(g.p.components()),
		FrontParts:     tierFronts(),
		TakeRecords:    variables.TakeProjectRecords,
		SweepAccounts:  release.DeleteUnusedAccounts,
		PushBinary:     g.p.pushBinary,
		DeployAndRoute: g.p.services().DeployAndRoute,
		TearDown:       g.p.services().TearDown,
	}, nil
}

func (g bootstrapGate) openTierBootstrap(ctx context.Context, tier environment.Tier) (bootstrap.Engine, error) {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return bootstrap.Engine{}, err
	}
	return withNetworkPatches(ctx, b, tier, "", transform.Result{})
}

func (g bootstrapGate) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	b, err := g.openTierBootstrap(ctx, tier)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	return b.Describe(ctx, tier)
}

func (g bootstrapGate) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	b, err := g.openTierBootstrap(ctx, req.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	return b.Plan(ctx, req)
}

func (g bootstrapGate) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	b, err := g.openTierBootstrap(ctx, req.Tier)
	if err != nil {
		return err
	}
	return b.Apply(ctx, req, progress)
}

func (g bootstrapGate) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	return b.PlanRemove(ctx, tier)
}

func (g bootstrapGate) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.Remove(ctx, tier, progress)
}

func (g bootstrapGate) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	b, err := g.openTierBootstrap(ctx, described.Tier)
	if err != nil {
		return nil, err
	}
	return bootstrap.DescribeParts(ctx, b, described)
}
