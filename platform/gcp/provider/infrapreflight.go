package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (p *Provider) PlanInfraPreflight(ctx context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	parts, err := p.listPartsResourcesNeed(pre)
	if err != nil || len(parts) == 0 {
		return provider.Plan{}, err
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	groups, err := b.PlanParts(ctx, pre.Deploy.Tier, parts)
	return provider.Plan{Groups: groups}, err
}

func (p *Provider) ApplyInfraPreflight(ctx context.Context, pre provider.DeployPreflight) error {
	parts, err := p.listPartsResourcesNeed(pre)
	if err != nil || len(parts) == 0 {
		return err
	}
	if err := p.addParts(ctx, pre, parts); err != nil {
		return err
	}
	return stackrecords.AddPartsUsed(ctx, p.KeyValues(), pre.Deploy.Tier, pre.Deploy.Slug, parts)
}

func (p *Provider) listPartsResourcesNeed(pre provider.DeployPreflight) ([]string, error) {
	if len(pre.Resources) == 0 {
		return nil, nil
	}
	return deployUse{deploy: pre.Deploy, resources: pre.Resources}.partNames(p.components())
}
