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

const unwindWindow = 60 * time.Second

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
	displaced, err := displacedBy(ctx, l, pointer)
	if err != nil {
		return err
	}
	if err := l.Promote(ctx, promoted, pointer, progress); err != nil {
		return err
	}
	for i, routed := range routers {
		err := routed.stack.Flip(ctx, flips[i], progress)
		var unserved router.Unserved
		if errors.As(err, &unserved) {
			return errors.Join(err, unwind(ctx, l, pointer, promoted.PromotionID, displaced, routers[:i]))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func displacedBy(ctx context.Context, l projectLedger, pointer string) (*router.Promotion, error) {
	active, found, err := l.active(ctx, pointer)
	if err != nil || !found {
		return nil, err
	}
	return &active, nil
}

func unwind(ctx context.Context, l projectLedger, pointer, promotionID string, displaced *router.Promotion, flipped []appRouter) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), unwindWindow)
	defer stop()
	if err := l.Unpromote(ctx, promotionID, pointer); err != nil {
		return fmt.Errorf("the ledger still names promotion %s on %s, which not every router serves: %w", promotionID, pointerName(pointer), err)
	}
	var errs []error
	for _, routed := range flipped {
		if err := restore(ctx, l, pointer, displaced, routed); err != nil {
			errs = append(errs, fmt.Errorf("a router still serves promotion %s on %s, which the ledger no longer names: %w", promotionID, pointerName(pointer), err))
		}
	}
	return errors.Join(errs...)
}

func restore(ctx context.Context, l projectLedger, pointer string, displaced *router.Promotion, routed appRouter) error {
	if displaced == nil {
		active, err := l.ActivePromotionID(ctx, pointer)
		if err != nil || active != "" {
			return err
		}
		return routed.stack.RemovePointer(ctx, pointer, progress.DiscardProgress())
	}
	records, err := l.records(ctx, *displaced, routed.apps)
	if err != nil {
		return err
	}
	err = routed.stack.Flip(ctx, router.Flip{
		Pointer:     pointer,
		Promotion:   *displaced,
		Records:     records,
		StillActive: stillActive(l, pointer, displaced.PromotionID),
	}, progress.DiscardProgress())
	var moved movedPromotion
	if errors.As(err, &moved) {
		return nil
	}
	return err
}

func stillActive(l projectLedger, pointer, promotionID string) func(context.Context) error {
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
			promotionID, pointerName(pointer), activeOr(active))}}
	}
}

func pointerName(pointer string) string {
	if pointer == "" {
		return router.DefaultPointer
	}
	return pointer
}

func activeOr(active string) string {
	if active == "" {
		return "nothing"
	}
	return active
}
