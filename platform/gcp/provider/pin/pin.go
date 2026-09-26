package pin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
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
	progress edge.Progress,
) error {
	var pinning []edge.DeploymentRecord
	for _, app := range slices.Sorted(maps.Keys(promotion.Builds)) {
		identity := promotion.Builds[app]
		record, held, err := ledger.Record(ctx, app, identity)
		if err != nil {
			return err
		}
		if !held {
			return refusal.Refuse(refusal.CodeInvalid,
				"promotion %s names build %s of %s, and this project's ledger staged no record for it, so nothing says which revision that build stood up",
				promotion.PromotionID, identity, app)
		}
		if len(record.Revisions) == 0 {
			return refusal.Refuse(refusal.CodeInvalid,
				"build %s of %s recorded no revision, and a promotion on Cloud Run is a traffic pin onto the revision the build stood up: "+
					"re-deploy %s so its release records one, then promote that",
				identity, app, app)
		}
		pinning = append(pinning, record)
	}
	if err := ledger.Promote(ctx, promotion, pointer, progress); err != nil {
		return err
	}
	for _, record := range pinning {
		for _, service := range slices.Sorted(maps.Keys(record.Revisions)) {
			revision := record.Revisions[service]
			if progress != nil {
				progress.Detail("Pinning " + service + " to " + revision)
			}
			if err := pins.Pin(ctx, service, revision); err != nil {
				if undo := ledger.Unpromote(ctx, promotion.PromotionID, pointer); undo != nil {
					return errors.Join(err, fmt.Errorf("the ledger still names promotion %s, which Cloud Run never finished pinning: %w", promotion.PromotionID, undo))
				}
				return err
			}
		}
	}
	return nil
}
