package pin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type Pins interface {
	Pin(ctx context.Context, service, revision string) error
}

func Flip(
	ctx context.Context,
	l *ledger.Ledger,
	pins Pins,
	flip router.Flip,
	progress progress.Progress,
) error {
	promotion, pointer := flip.Promotion, flip.Pointer
	var pinning []router.DeploymentRecord
	for _, app := range slices.Sorted(maps.Keys(promotion.Builds)) {
		identity := promotion.Builds[app]
		record, staged, err := l.Record(ctx, app, identity)
		if err != nil {
			return err
		}
		if !staged {
			return refusal.Refuse(refusal.CodeInvalid,
				"promotion %s names build %s of %s, and this project's ledger staged no record for it, so nothing says which revision that build deployed",
				promotion.PromotionID, identity, app)
		}
		if len(record.Revisions) == 0 {
			return refusal.Refuse(refusal.CodeInvalid,
				"build %s of %s recorded no revision, and a promotion on Cloud Run is a traffic pin onto the revision the build deployed: "+
					"re-deploy %s so its release records one, then promote that",
				identity, app, app)
		}
		pinning = append(pinning, record)
	}
	if err := l.Promote(ctx, promotion, pointer, progress); err != nil {
		return err
	}
	if err := flip.RefuseInactive(ctx); err != nil {
		return unpromote(ctx, l, promotion.PromotionID, pointer, err)
	}
	pinned := false
	for _, record := range pinning {
		for _, service := range slices.Sorted(maps.Keys(record.Revisions)) {
			revision := record.Revisions[service]
			if progress != nil {
				progress.Say("Pinning all of " + record.App + "'s traffic to revision " + revision + " of Cloud Run service " + service)
			}
			if err := pins.Pin(ctx, service, revision); err != nil {
				if !pinned {
					err = router.Unserved{Err: err}
				}
				return unpromote(ctx, l, promotion.PromotionID, pointer, err)
			}
			pinned = true
		}
	}
	return nil
}

func unpromote(ctx context.Context, l *ledger.Ledger, promotionID, pointer string, err error) error {
	if undo := l.Unpromote(ctx, promotionID, pointer); undo != nil {
		return errors.Join(err, fmt.Errorf("the ledger still names promotion %s, which Cloud Run never finished pinning: %w", promotionID, undo))
	}
	return err
}
