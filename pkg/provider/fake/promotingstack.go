package fake

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type PromotingStack struct {
	router.Stack
	Ledger *ledger.Ledger
}

func (s PromotingStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	flip.Records = map[string]router.DeploymentRecord{}
	for app, build := range flip.Promotion.Builds {
		record, staged, err := s.Ledger.Record(ctx, app, build)
		if err != nil {
			return err
		}
		if !staged {
			return fmt.Errorf("promote %s: the ledger staged no record for %s/%s", flip.Promotion.PromotionID, app, build)
		}
		flip.Records[app] = record
	}
	over, err := s.Ledger.ActivePromotionID(ctx, flip.Pointer)
	if err != nil {
		return err
	}
	if _, err := s.Ledger.Promote(ctx, flip.Promotion, flip.Pointer, over); err != nil {
		return err
	}
	asked := flip.StillActive
	flip.StillActive = func(ctx context.Context) error {
		if asked != nil {
			if err := asked(ctx); err != nil {
				return err
			}
		}
		active, err := s.Ledger.ActivePromotionID(ctx, flip.Pointer)
		if err != nil {
			return err
		}
		if active != flip.Promotion.PromotionID {
			return refusal.Refuse(refusal.CodeBusy, "promotion %s is no longer active; the ledger names %q", flip.Promotion.PromotionID, active)
		}
		return nil
	}
	err = s.Stack.Flip(ctx, flip, progress)
	var unserved router.Unserved
	if errors.As(err, &unserved) {
		return errors.Join(err, s.Ledger.Unpromote(ctx, flip.Promotion.PromotionID, flip.Pointer))
	}
	return err
}
