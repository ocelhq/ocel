package controltest

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func EngineCalls(ctx context.Context, p provider.Provider) (map[string]func() error, error) {
	hooks := p.Hooks()
	boot, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		return nil, err
	}
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction}}
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierPreview, Env: "pr-1"}
	return map[string]func() error{
		"Stacks().Plan": func() error {
			_, err := p.Stacks().Plan(ctx, provider.StackSpec{}, progress.Discard())
			return err
		},
		"Runtime().Arch": func() error {
			_, err := p.Runtime().Arch(ctx, "web", "")
			return err
		},
		"Bootstrap().Plan": func() error {
			_, err := boot.Plan(ctx, provider.BootstrapRequest{Tier: environment.TierProduction})
			return err
		},
		"Bootstrap().Apply": func() error {
			return boot.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction}, progress.Discard())
		},
		"Bootstrap().PlanRemove":  func() error { _, err := boot.PlanRemove(ctx, environment.TierProduction); return err },
		"Bootstrap().Remove":      func() error { return boot.Remove(ctx, environment.TierProduction, progress.Discard()) },
		"Hooks().Preflight.Plan":  func() error { _, err := hooks.Preflight.Plan(ctx, pre); return err },
		"Hooks().Preflight.Apply": func() error { return hooks.Preflight.Apply(ctx, pre) },
		"Hooks().ProgramEdge": func() error {
			_, err := hooks.ProgramEdge(ctx, provider.EdgeProgramRequest{Tier: environment.TierProduction, Kind: edges.DefaultKind})
			return err
		},
		"Hooks().EnsureImageRegistry": func() error {
			_, err := hooks.EnsureImageRegistry(ctx, environment.TierProduction)
			return err
		},
		"Hooks().OpenRegistryImages": func() error {
			_, err := hooks.OpenRegistryImages(ctx, provider.RegistryTarget{})
			return err
		},
		"Hooks().BindingAccess.ForwardPorts": func() error {
			_, err := hooks.BindingAccess.ForwardPorts(ctx, provider.PortForwardRequest{Tier: environment.TierProduction, Bindings: []provider.Binding{{Name: "main", Type: provider.BindingPostgres}}}, progress.Discard())
			return err
		},
		"Hooks().Cost.Inventory": func() error {
			_, err := hooks.Cost.Inventory(ctx, provider.InventoryRequest{Edge: edges.DefaultKind, Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: "shop", Env: "prod"}})
			return err
		},
		"Hooks().Retirement.Plan":   func() error { _, err := hooks.Retirement.Plan(ctx, live, progress.Discard()); return err },
		"Hooks().Retirement.Retire": func() error { return hooks.Retirement.Retire(ctx, live, progress.Discard()) },
		"Connector().Install": func() error {
			_, err := p.Connector().Install(ctx, provider.ConnectorInstall{}, progress.Discard())
			return err
		},
		"Connector().Remove": func() error { return p.Connector().Remove(ctx, progress.Discard()) },
	}, nil
}
