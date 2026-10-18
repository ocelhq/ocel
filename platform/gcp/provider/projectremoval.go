package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const reasonALBReleased = "the project stops using the tier's load balancer, which leaves with the last project behind it"

func (p *Provider) PlanProjectRemoval(ctx context.Context, tier environment.Tier, project string) ([]provider.ChangeGroup, error) {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return nil, err
	}
	return planProjectRemoval(ctx, b, tier, project)
}

func (p *Provider) RemoveProject(ctx context.Context, tier environment.Tier, project string, log progress.Log) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	if err := releaseALB(ctx, b, tier, project, log); err != nil {
		return err
	}
	return variables.RemoveProject(ctx, b, tier, project, log)
}

func planProjectRemoval(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) ([]provider.ChangeGroup, error) {
	var changes []provider.Change
	consumer, err := records.ReadOrEmpty(ctx, b.Records, albpart.ConsumerAt(tier, project))
	if err != nil {
		return nil, err
	}
	if len(consumer.Value) > 0 {
		changes = append(changes, provider.Change{Kind: "part", Name: albpart.PartName, Action: provider.ActionUpdate, Reason: reasonALBReleased})
	}
	held, err := variables.PlanRemoveProject(ctx, b, tier, project)
	if err != nil {
		return nil, err
	}
	changes = append(changes, held...)
	if len(changes) == 0 {
		return nil, nil
	}
	return []provider.ChangeGroup{{
		Kind:    provider.StackGroupKind,
		Name:    string(gcloud.Vendor) + "/" + b.Clients.Project() + "/" + string(tier) + "/" + project,
		Action:  provider.ActionDelete,
		Changes: changes,
	}}, nil
}

func releaseALB(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, log progress.Log) error {
	consumer, err := records.ReadOrEmpty(ctx, b.Records, albpart.ConsumerAt(tier, project))
	if err != nil || len(consumer.Value) == 0 {
		return err
	}
	return albpart.Release(ctx, b, tier, project, true, log)
}
