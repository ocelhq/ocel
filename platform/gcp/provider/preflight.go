package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	parts, err := p.components().Preflight(pre.Resources)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(pre.Uses, func(uses buildoutput.Uses) bool { return uses.Revalidation }) {
		parts = append(parts, tagDatabasePart)
	}
	if pre.Edge == alb.Kind {
		parts = append(parts, albPart)
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

func (p *Provider) leaveALB(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Dry || pre.Deploy.Slug == "" {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return releaseALB(ctx, b, pre.Deploy.Tier, pre.Deploy.Slug, false, pre.Progress)
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
