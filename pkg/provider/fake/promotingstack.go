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

func (s PromotingStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	move.Records = map[string]router.ReleaseRecord{}
	for app, release := range move.Promotion.Releases {
		record, staged, err := s.Ledger.Record(ctx, app, release)
		if err != nil {
			return err
		}
		if !staged {
			return fmt.Errorf("promote %s: the ledger staged no record for %s/%s", move.Promotion.PromotionID, app, release)
		}
		move.Records[app] = record
	}
	replaces, err := s.Ledger.ActivePromotionID(ctx, move.Pointer)
	if err != nil {
		return err
	}
	if _, err := s.Ledger.Promote(ctx, move.Promotion, move.Pointer, replaces); err != nil {
		return err
	}
	asked := move.StillActive
	move.StillActive = func(ctx context.Context) error {
		if asked != nil {
			if err := asked(ctx); err != nil {
				return err
			}
		}
		active, err := s.Ledger.ActivePromotionID(ctx, move.Pointer)
		if err != nil {
			return err
		}
		if active != move.Promotion.PromotionID {
			return refusal.Refuse(refusal.CodeBusy, "promotion %s is no longer active; the ledger names %q", move.Promotion.PromotionID, active)
		}
		return nil
	}
	err = s.Stack.MovePointer(ctx, move, progress)
	var unserved router.Unserved
	if errors.As(err, &unserved) {
		return errors.Join(err, s.Ledger.Unpromote(ctx, move.Promotion.PromotionID, move.Pointer))
	}
	return err
}
