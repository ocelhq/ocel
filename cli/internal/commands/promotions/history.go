package promotions

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func promotionHistory(ctx context.Context, check *run.Span, prov *providerprocess.Provider, cfg *project.Project) ([]*contractv1.PromotionHistoryEntry, error) {
	unit := check.Unit(cfg.Slug, progress.Reading.Title("the promotion history of production"))
	var listed *contractv1.ListPromotionsResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		listed, err = client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{
			Slug: cfg.Slug,
			Edge: cfg.EdgeSelection(),
		})
		return err
	})
	unit.End(err)
	return listed.GetPromotions(), err
}

func activePromotion(history []*contractv1.PromotionHistoryEntry) *contractv1.Promotion {
	for _, entry := range history {
		if entry.GetActive() {
			return entry.GetPromotion()
		}
	}
	return nil
}

func deployedIdentities(identityByApp map[string]string) string {
	if len(identityByApp) == 0 {
		return "—"
	}
	apps := make([]string, 0, len(identityByApp))
	for app := range identityByApp {
		apps = append(apps, app)
	}
	slices.Sort(apps)

	pairs := make([]string, 0, len(apps))
	for _, app := range apps {
		pairs = append(pairs, app+"="+identityByApp[app])
	}
	return strings.Join(pairs, " ")
}
