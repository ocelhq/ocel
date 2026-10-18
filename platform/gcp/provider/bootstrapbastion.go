package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const reasonBastionOnDemand = "a build's port forwards made it on first use, so the bootstrap's own list does not name it"

func bastionRemovals(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]provider.Change, error) {
	at := bastion{clients: b.Clients}
	var changes []provider.Change
	service, err := at.read(ctx, tier)
	if err != nil {
		return nil, err
	}
	if service != nil {
		changes = append(changes, provider.Change{Kind: string(bootstrap.KindService), Name: b.Clients.Bastion(tier), Action: provider.ActionDelete, Reason: reasonBastionOnDemand})
	}
	account, err := b.Clients.AccountExists(ctx, b.Clients.BastionAccount(tier))
	if err != nil {
		return nil, err
	}
	if account {
		changes = append(changes, provider.Change{Kind: string(bootstrap.KindServiceAccount), Name: b.Clients.BastionAccount(tier), Action: provider.ActionDelete, Reason: reasonBastionOnDemand})
	}
	return changes, nil
}

func removeBastion(ctx context.Context, b bootstrap.Engine, tier environment.Tier, progress progress.Log) error {
	return bastion{clients: b.Clients, tearDown: b.TearDown}.remove(ctx, tier, progress)
}

func describeBastionPart(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	service, err := bastion{clients: b.Clients}.read(ctx, tier)
	if err != nil || service == nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:    bootstrap.BastionPart,
		Summary: bootstrap.BastionSummary,
		Current: true,
		Resources: []provider.PartResource{
			{Kind: string(bootstrap.KindService), ID: b.Clients.Bastion(tier)},
			{Kind: string(bootstrap.KindServiceAccount), ID: b.Clients.BastionAccount(tier)},
		},
		Shape: &provider.PartShape{Vendor: string(gcloud.Vendor), Region: b.Clients.Region, Resources: []pricing.Shaped{{
			Name:       b.Clients.Bastion(tier),
			Type:       cost.CloudRunServiceType,
			Properties: serviceProperties(true, 0, run.RevisionMemory, run.IngressInternal),
		}}},
	}, true, nil
}
