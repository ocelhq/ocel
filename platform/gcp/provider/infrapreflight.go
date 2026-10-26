package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (p *Provider) PlanInfraPreflight(ctx context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	parts, err := p.listPartsResourcesNeed(ctx, pre)
	if err != nil || len(parts) == 0 {
		return provider.Plan{}, err
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	if p.readsServices(parts, nil) {
		switching, err := b.ServicesToSwitchOn(ctx, pre.Deploy.Tier, parts, nil)
		if err != nil {
			return provider.Plan{}, err
		}
		plan.Groups = b.PlanSwitchOn(switching)
	}
	if b, err = p.withDeployNetworkPatches(ctx, b, pre.Deploy, parts); err != nil {
		return provider.Plan{}, err
	}
	groups, err := b.PlanParts(ctx, pre.Deploy.Tier, parts)
	if err != nil {
		return provider.Plan{}, err
	}
	plan.Groups = append(plan.Groups, groups...)
	return plan, nil
}

func (p *Provider) ApplyInfraPreflight(ctx context.Context, pre provider.DeployPreflight) error {
	parts, err := p.listPartsResourcesNeed(ctx, pre)
	if err != nil || len(parts) == 0 {
		return err
	}
	if err := p.switchOnServices(ctx, pre, parts, nil); err != nil {
		return err
	}
	if err := p.addParts(ctx, pre, parts); err != nil {
		return err
	}
	return stackrecords.AddPartsUsed(ctx, p.KeyValues(), pre.Deploy.Tier, pre.Deploy.Slug, parts)
}

func (p *Provider) listPartsResourcesNeed(ctx context.Context, pre provider.DeployPreflight) ([]string, error) {
	if len(pre.Resources) == 0 {
		return nil, nil
	}
	parts, err := deployUse{deploy: pre.Deploy, resources: pre.Resources}.partNames(p.components())
	if err != nil {
		return nil, err
	}
	return p.withoutUnusedNetwork(ctx, pre, parts)
}
