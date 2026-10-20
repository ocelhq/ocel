package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func describeProduction(front edge.Facts, state stackrecords.EdgeState, active router.Promotion) *contractv1.ProjectEnvironment {
	return &contractv1.ProjectEnvironment{
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		PromotionId: active.PromotionID,
		Releases:    active.Releases,
		Urls:        formatHostURLs(front, state.PointerHostnames(router.DefaultPointer)),
	}
}

func describePreviews(ctx context.Context, front edge.Facts, store records.Store, slug string, previews []stackrecords.Environment) ([]*contractv1.ProjectEnvironment, error) {
	promotions := ledger.New(store, environment.TierPreview, slug)
	described := make([]*contractv1.ProjectEnvironment, 0, len(previews))
	for _, preview := range previews {
		active, _, err := promotions.ReadActive(ctx, preview.Identity)
		if err != nil {
			return nil, err
		}
		described = append(described, &contractv1.ProjectEnvironment{
			Environment: &environmentv1.Environment{
				Tier:      environmentv1.Tier_TIER_PREVIEW,
				Identity:  preview.Identity,
				Lifecycle: encodeLifecycle(preview.Lifecycle),
				Label:     preview.Label,
			},
			PromotionId: active.PromotionID,
			Releases:    active.Releases,
			Urls:        formatHostURLs(front, edge.ListPreviewHostnames(preview.Aliases)),
		})
	}
	return described, nil
}
