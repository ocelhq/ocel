package pin

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Pins interface {
	Pin(ctx context.Context, service, revision string) error
}

func Promote(
	ctx context.Context,
	ledger *kitledger.Ledger,
	pins Pins,
	promotion edge.Promotion,
	pointer string,
	report edge.Reporter,
) error {
	for _, app := range slices.Sorted(maps.Keys(promotion.Builds)) {
		identity := promotion.Builds[app]
		record, held, err := ledger.Record(ctx, app, identity)
		if err != nil {
			return err
		}
		if !held {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"promotion %s names build %s of %s, and this project's ledger staged no record for it, so nothing says which revision that build stood up",
				promotion.PromotionID, identity, app)
		}
		if len(record.Revisions) == 0 {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"build %s of %s recorded no revision, and a promotion on Cloud Run is a traffic pin onto the revision the build stood up: "+
					"re-deploy %s so its release records one, then promote that",
				identity, app, app)
		}
		for _, service := range slices.Sorted(maps.Keys(record.Revisions)) {
			revision := record.Revisions[service]
			if report != nil {
				report.Detail("Pinning " + service + " to " + revision)
			}
			if err := pins.Pin(ctx, service, revision); err != nil {
				return err
			}
		}
	}
	return ledger.Promote(ctx, promotion, pointer, report)
}
