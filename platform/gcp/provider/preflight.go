package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func (p *Provider) PlanPreflight(ctx context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	parts, err := p.partsUsedBy(pre)
	if err != nil {
		return provider.Plan{}, err
	}
	writes := writesProjectDatabase(pre)
	if !writes && len(parts) == 0 {
		return provider.Plan{}, nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	if writes {
		group, err := variables.PlanProjectWritableDatabase(ctx, b, pre)
		if err != nil {
			return provider.Plan{}, err
		}
		if group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	groups, err := b.PlanParts(ctx, pre.Deploy.Tier, parts)
	if err != nil {
		return provider.Plan{}, err
	}
	plan.Groups = append(plan.Groups, groups...)
	return plan, nil
}

func (p *Provider) ApplyPreflight(ctx context.Context, pre provider.DeployPreflight) error {
	parts, err := p.partsUsedBy(pre)
	if err != nil {
		return err
	}
	if err := p.ensureProjectWritableDatabase(ctx, pre); err != nil {
		return err
	}
	if pre.Edge != alb.Kind {
		if err := p.leaveALB(ctx, pre); err != nil {
			return err
		}
	}
	if err := p.addParts(ctx, pre, parts); err != nil {
		return err
	}
	if pre.Deploy.Slug == "" {
		return nil
	}
	slices.Sort(parts)
	return stackrecords.RecordPartsUsed(ctx, p.KeyValues(), pre.Deploy.Tier, pre.Deploy.Slug, parts)
}

func (p *Provider) partsUsedBy(pre provider.DeployPreflight) ([]string, error) {
	parts, err := p.components().Preflight(pre.Resources)
	if err != nil {
		return nil, err
	}
	if pre.Edge == alb.Kind {
		parts = append(parts, albpart.PartName)
	}
	return parts, nil
}

func writesProjectDatabase(pre provider.DeployPreflight) bool {
	revalidates := slices.ContainsFunc(pre.Uses, func(uses buildoutput.Uses) bool { return uses.Revalidation })
	return pre.Deploy.Slug != "" && (revalidates || topic.DeclaresTopics(pre.Resources))
}

func (p *Provider) ensureProjectWritableDatabase(ctx context.Context, pre provider.DeployPreflight) error {
	if !writesProjectDatabase(pre) {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return variables.EnsureProjectWritableDatabase(ctx, b, pre, task.RunLayout(), tagLayout())
}

func (p *Provider) leaveALB(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Deploy.Slug == "" {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return albpart.Release(ctx, b, pre.Deploy.Tier, pre.Deploy.Slug, false, pre.Progress)
}

func (p *Provider) addParts(ctx context.Context, pre provider.DeployPreflight, names []string) error {
	if len(names) == 0 {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.AddParts(ctx, pre, names)
}
