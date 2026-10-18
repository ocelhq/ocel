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

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	parts, err := p.components().Preflight(pre.Resources)
	if err != nil {
		return err
	}
	if err := p.ensureProjectWritableDatabase(ctx, pre); err != nil {
		return err
	}
	if pre.Edge == alb.Kind {
		parts = append(parts, albpart.PartName)
	} else if err := p.leaveALB(ctx, pre); err != nil {
		return err
	}
	if err := p.addParts(ctx, pre, parts); err != nil {
		return err
	}
	if pre.Dry || pre.Deploy.Slug == "" {
		return nil
	}
	slices.Sort(parts)
	return stackrecords.RecordPartsUsed(ctx, p.KeyValues(), pre.Deploy.Tier, pre.Deploy.Slug, parts)
}

func (p *Provider) ensureProjectWritableDatabase(ctx context.Context, pre provider.DeployPreflight) error {
	revalidates := slices.ContainsFunc(pre.Uses, func(uses buildoutput.Uses) bool { return uses.Revalidation })
	if pre.Deploy.Slug == "" || !revalidates && !topic.DeclaresTopics(pre.Resources) {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return variables.EnsureProjectWritableDatabase(ctx, b, pre, task.RunLayout(), tagLayout())
}

func (p *Provider) leaveALB(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Dry || pre.Deploy.Slug == "" {
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
