package vps

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) PlanProjectRemoval(ctx context.Context, tier environment.Tier, project string) ([]provider.ChangeGroup, error) {
	project = naming.Sanitize(project)
	record, held, err := p.readFronting(ctx, tier)
	if err != nil || !held || !slices.Contains(record.Projects, project) {
		return nil, err
	}
	changes := []provider.Change{{Kind: "kept files", Name: project, Action: provider.ActionDelete}}
	if len(record.Projects) == 1 {
		changes = append(changes, provider.Change{Kind: "releases store", Name: record.Store, Action: provider.ActionDelete, Reason: "no other project on this box's tier sets `cache`"})
	}
	return []provider.ChangeGroup{{
		Kind:    provider.StackGroupKind,
		Name:    string(Vendor) + "/" + string(tier) + "/" + project,
		Action:  provider.ActionDelete,
		Changes: changes,
	}}, nil
}

func (p *Provider) RemoveProject(ctx context.Context, tier environment.Tier, project string, _ progress.Log) error {
	return p.forgetFrontedProject(ctx, tier, naming.Sanitize(project))
}
