package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

func tierParts() []bootstrap.RemovablePart {
	return []bootstrap.RemovablePart{
		{Part: provider.Part{Name: network.PartName, Summary: network.Summary}, Deployed: network.NewPart},
		{Part: provider.Part{Name: task.PartName, Summary: task.Summary}, Deployed: task.NewPart},
		{Part: provider.Part{Name: tagDatabasePart, Summary: tagDatabaseSummary}, Deployed: newTagDatabasePart},
		{Part: provider.Part{Name: albPart, Summary: albSummary}, Deployed: newALBPart},
		{
			Part:     provider.Part{Name: envSourceSyncPart, Summary: envSourceSyncSummary},
			Plan:     envSourceSyncRemovals,
			Remove:   removeEnvSourceSyncPart,
			Take:     removeEnvSourceSync,
			Describe: describeEnvSourceSyncPart,
			Work: func(b bootstrap.Engine, tier environment.Tier) stackrecords.PartWork {
				return envSourceSyncWork(b, tier, "")
			},
		},
		{
			Part:     provider.Part{Name: bootstrap.BastionPart, Summary: bootstrap.BastionSummary},
			Plan:     bastionRemovals,
			Remove:   removeBastion,
			Take:     removeBastion,
			Describe: describeBastionPart,
		},
	}
}

func tierAccounts() []bootstrap.Account {
	return []bootstrap.Account{
		{Name: names.Names.EnvSourceSyncAccount, Purpose: syncPurpose},
		{Name: names.Names.PushAccount, Purpose: task.PushPurpose},
		{Name: names.Names.RealtimeAccount, Purpose: realtimePurpose},
	}
}

func tierFronts() bootstrap.FrontParts {
	return bootstrap.FrontParts{
		Adopt: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind, out edge.BootstrapOutput, progress progress.Log) error {
			return adoptEdgeOffers(ctx, b.Clients, b.Records, tier, kind, out, progress)
		},
		Forget: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) error {
			return forgetEdgeOffers(ctx, b.Clients, b.Records, tier, kind)
		},
		RaiseAssets: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) error {
			return raiseAssetStore(ctx, b.Clients, tier, kind)
		},
		TakeAssets: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
			return takeAssetStore(ctx, b.Clients, tier)
		},
		PlanAssets: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
			return plannedAssetStore(ctx, b.Clients, tier, kind)
		},
		PlanAssetsRemoval: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
			return plannedAssetStoreRemoval(ctx, b.Clients, tier, kind)
		},
	}
}
