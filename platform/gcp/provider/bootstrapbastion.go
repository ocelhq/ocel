package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const reasonBastionOnDemand = "a build's port forwards made it on first use, so the bootstrap's own list does not name it"

func (b bootstrap) bastionRemovals(ctx context.Context, tier environment.Tier) ([]provider.Change, error) {
	at := bastion{clients: b.clients}
	var changes []provider.Change
	service, err := at.read(ctx, tier)
	if err != nil {
		return nil, err
	}
	if service != nil {
		changes = append(changes, provider.Change{Kind: string(KindService), Name: b.clients.Bastion(tier), Action: provider.ActionDelete, Reason: reasonBastionOnDemand})
	}
	account, err := b.clients.accountExists(ctx, b.clients.BastionAccount(tier))
	if err != nil {
		return nil, err
	}
	if account {
		changes = append(changes, provider.Change{Kind: string(KindServiceAccount), Name: b.clients.BastionAccount(tier), Action: provider.ActionDelete, Reason: reasonBastionOnDemand})
	}
	return changes, nil
}

func (b bootstrap) removeBastion(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	return bastion{clients: b.clients, tearDown: b.tearDown}.remove(ctx, tier, progress)
}
