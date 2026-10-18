package cloudflareorigin

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

var FrontParts = bootstrap.FrontParts{
	Adopt: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind, out edge.BootstrapOutput, progress progress.Log) error {
		return AdoptOffers(ctx, b.Clients, b.Records, tier, kind, out, progress)
	},
	Forget: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) error {
		return ForgetOffers(ctx, b.Clients, b.Records, tier, kind)
	},
	RaiseAssets: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) error {
		return RaiseAssetStore(ctx, b.Clients, tier, kind)
	},
	TakeAssets: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
		return TakeAssetStore(ctx, b.Clients, tier)
	},
	PlanAssets: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
		return PlannedAssetStore(ctx, b.Clients, tier, kind)
	},
	PlanAssetsRemoval: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
		return PlannedAssetStoreRemoval(ctx, b.Clients, tier, kind)
	},
}
