package cloudflareorigin

import (
	"context"
	"time"

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
		held, err := ReadAssets(ctx, b, tier, kind)
		return held.Planned(b.Clients, tier, kind, time.Now()), err
	},
	PlanAssetsRemoval: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
		held, err := ReadAssets(ctx, b, tier, kind)
		return held.PlannedRemoval(b.Clients, tier, kind), err
	},
	RaiseOriginSecret: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind, progress progress.Log) error {
		return EnsureOriginSecret(ctx, b.Clients, tier, kind, time.Now(), progress)
	},
	HasOriginSecret: func(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) (bool, error) {
		if b.IsOff(bootstrap.SecretManagerAPI) {
			return false, nil
		}
		_, found, err := ReadOriginSecret(ctx, b.Clients, tier, kind)
		return found, err
	},
}
