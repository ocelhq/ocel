package providerserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	unwindWindow    = 60 * time.Second
	restoreAttempts = 8
)

type appRouter struct {
	stack router.Stack
	apps  []string
}

type movedPromotion struct{ refusal.Refusal }

func (m movedPromotion) Unwrap() error { return m.Refusal }

func promote(ctx context.Context, l projectLedger, pointer string, promoted router.Promotion, routers []appRouter, progress progress.Progress) error {
	flips := make([]router.Flip, len(routers))
	for i, routed := range routers {
		records, err := l.records(ctx, promoted, routed.apps)
		if err != nil {
			return err
		}
		flips[i] = router.Flip{Pointer: pointer, Promotion: promoted, Records: records, StillActive: stillActive(l, pointer, promoted.PromotionID)}
	}
	if err := l.Promote(ctx, promoted, pointer, progress); err != nil {
		return err
	}
	for i, routed := range routers {
		err := routed.stack.Flip(ctx, flips[i], progress)
		var unserved router.Unserved
		if errors.As(err, &unserved) {
			return errors.Join(err, unwind(ctx, l, pointer, promoted.PromotionID, routers[:i]))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func unwind(ctx context.Context, l projectLedger, pointer, promotionID string, flipped []appRouter) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), unwindWindow)
	defer stop()
	if err := l.Unpromote(ctx, promotionID, pointer); err != nil {
		return fmt.Errorf("the ledger still names promotion %s on %s, which not every router serves: %w", promotionID, router.ResolvePointer(pointer), err)
	}
	var errs []error
	for _, routed := range flipped {
		if err := restore(ctx, l, pointer, routed); err != nil {
			errs = append(errs, fmt.Errorf("a router still serves promotion %s on %s, which the ledger no longer names: %w", promotionID, router.ResolvePointer(pointer), err))
		}
	}
	return errors.Join(errs...)
}

func restore(ctx context.Context, l projectLedger, pointer string, routed appRouter) error {
	for range restoreAttempts {
		active, found, err := l.ReadActive(ctx, pointer)
		if err != nil {
			return err
		}
		if !found {
			if err := routed.stack.RemovePointer(ctx, pointer, progress.DiscardProgress()); err != nil {
				return err
			}
			named, err := l.ActivePromotionID(ctx, pointer)
			if err != nil || named == "" {
				return err
			}
			continue
		}
		records, err := l.records(ctx, active, routed.apps)
		if err != nil {
			return err
		}
		err = routed.stack.Flip(ctx, router.Flip{
			Pointer:     pointer,
			Promotion:   active,
			Records:     records,
			StillActive: stillActive(l, pointer, active.PromotionID),
		}, progress.DiscardProgress())
		var moved movedPromotion
		if !errors.As(err, &moved) {
			return err
		}
	}
	return fmt.Errorf("another deploy moved %s on each of %d attempts to serve what the ledger names there", router.ResolvePointer(pointer), restoreAttempts)
}

func stillActive(l projectLedger, pointer, promotionID string) router.StillActive {
	return func(ctx context.Context) error {
		active, err := l.ActivePromotionID(ctx, pointer)
		if err != nil {
			return err
		}
		if active == promotionID {
			return nil
		}
		return movedPromotion{refusal.Refusal{Code: refusal.CodeBusy, Message: fmt.Sprintf(
			"promotion %s is no longer active on %s, which now names %s: another deploy moved it while this one flipped, and this deploy stopped rather than serve a release the ledger no longer names. Re-run this deploy once the other one has finished if its release should serve",
			promotionID, router.ResolvePointer(pointer), activeOr(active))}}
	}
}

func activeOr(active string) string {
	if active == "" {
		return "nothing"
	}
	return active
}
