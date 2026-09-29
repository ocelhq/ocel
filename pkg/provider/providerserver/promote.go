package providerserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	unpromoteWindow = 60 * time.Second
	restoreAttempts = 8
)

type appRouter struct {
	stack router.Stack
	apps  []string
}

type inactivePromotion struct{ refusal.Refusal }

func (i inactivePromotion) Unwrap() error { return i.Refusal }

type promoteRequest struct {
	pointer     string
	replaces    string
	rollsBackTo string
	promotion   router.Promotion
}

func (r promoteRequest) record(ctx context.Context, l projectLedger) ([]ledger.RecordedPromotion, error) {
	if r.rollsBackTo != "" {
		return l.Rollback(ctx, r.rollsBackTo, r.promotion, r.pointer, r.replaces)
	}
	return l.Promote(ctx, r.promotion, r.pointer, r.replaces)
}

func promote(ctx context.Context, l projectLedger, req promoteRequest, routers []appRouter, progress progress.Progress) ([]ledger.RecordedPromotion, error) {
	pointer, promoted := req.pointer, req.promotion
	flips := make([]router.Flip, len(routers))
	for i, routed := range routers {
		records, err := l.readRecords(ctx, promoted, routed.apps)
		if err != nil {
			return nil, err
		}
		flips[i] = router.Flip{Pointer: pointer, Promotion: promoted, Records: records, StillActive: newStillActive(l, pointer, promoted.PromotionID)}
	}
	dropped, err := req.record(ctx, l)
	if err != nil {
		return nil, err
	}
	if err := l.RewriteRecords(ctx, promoted); err != nil {
		return dropped, errors.Join(err, unpromote(ctx, l, pointer, promoted.PromotionID, nil))
	}
	for i, routed := range routers {
		err := routed.stack.Flip(ctx, flips[i], progress)
		var unserved router.Unserved
		if errors.As(err, &unserved) {
			return dropped, errors.Join(err, unpromote(ctx, l, pointer, promoted.PromotionID, routers[:i]))
		}
		if err != nil {
			return dropped, err
		}
	}
	return dropped, nil
}

func unpromote(ctx context.Context, l projectLedger, pointer, promotionID string, flipped []appRouter) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), unpromoteWindow)
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
		records, err := l.readRecords(ctx, active, routed.apps)
		if err != nil {
			return err
		}
		err = routed.stack.Flip(ctx, router.Flip{
			Pointer:     pointer,
			Promotion:   active,
			Records:     records,
			StillActive: newStillActive(l, pointer, active.PromotionID),
		}, progress.DiscardProgress())
		var inactive inactivePromotion
		if !errors.As(err, &inactive) {
			return err
		}
	}
	return fmt.Errorf("another deploy moved %s on each of %d attempts to serve what the ledger names there", router.ResolvePointer(pointer), restoreAttempts)
}

func newStillActive(l projectLedger, pointer, promotionID string) router.StillActive {
	return func(ctx context.Context) error {
		active, err := l.ActivePromotionID(ctx, pointer)
		if err != nil {
			return err
		}
		if active == promotionID {
			return nil
		}
		return inactivePromotion{refusal.Refusal{Code: refusal.CodeBusy, Message: fmt.Sprintf(
			"promotion %s is no longer active on %s, which now names %s: another deploy moved it while this one flipped, and this deploy stopped rather than serve a release the ledger no longer names. Re-run this deploy once the other one has finished if its release should serve",
			promotionID, router.ResolvePointer(pointer), describeActive(active))}}
	}
}

func describeActive(active string) string {
	if active == "" {
		return "nothing"
	}
	return active
}
