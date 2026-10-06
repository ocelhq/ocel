package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
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
	stack      router.Stack
	apps       []string
	hosts      []edge.PreviewHost
	superseded []edge.PreviewHost
	previous   []edge.PreviewHost
}

type inactivePromotion struct{ refusal.Refusal }

func (i inactivePromotion) Unwrap() error { return i.Refusal }

type promoteRequest struct {
	pointer     string
	hosts       []edge.PreviewHost
	superseded  []edge.PreviewHost
	previous    []edge.PreviewHost
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

func promote(ctx context.Context, l projectLedger, req promoteRequest, routers []appRouter, progress progress.Log) ([]ledger.RecordedPromotion, error) {
	pointer, promoted := req.pointer, req.promotion
	moves := make([]router.PointerMove, len(routers))
	for i, routed := range routers {
		records, err := l.readRecords(ctx, promoted, routed.apps)
		if err != nil {
			return nil, err
		}
		moves[i] = router.PointerMove{Pointer: pointer, Promotion: promoted, Records: records, Hosts: routed.hosts, Superseded: routed.superseded, StillActive: newStillActive(l, pointer, promoted.PromotionID)}
	}
	dropped, err := req.record(ctx, l)
	if err != nil {
		return nil, err
	}
	if err := l.RewriteRecords(ctx, promoted); err != nil {
		return dropped, errors.Join(err, unpromote(ctx, l, pointer, promoted.PromotionID, nil))
	}
	for i, routed := range routers {
		err := routed.stack.MovePointer(ctx, moves[i], progress)
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

func unpromote(ctx context.Context, l projectLedger, pointer, promotionID string, moved []appRouter) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), unpromoteWindow)
	defer stop()
	if err := l.Unpromote(ctx, promotionID, pointer); err != nil {
		return fmt.Errorf("the ledger still names promotion %s on %s, which not every router serves: %w", promotionID, router.ResolvePointer(pointer), err)
	}
	var errs []error
	for _, routed := range moved {
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
			if err := routed.stack.RemovePointer(ctx, router.PointerRemoval{Pointer: pointer, Hosts: routed.hosts}, progress.Discard()); err != nil {
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
		err = routed.stack.MovePointer(ctx, router.PointerMove{
			Pointer:     pointer,
			Promotion:   active,
			Records:     records,
			Hosts:       routed.previous,
			Superseded:  routed.hosts,
			StillActive: newStillActive(l, pointer, active.PromotionID),
		}, progress.Discard())
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
			"promotion %s is no longer active on %s, which now names %s: another deploy moved it while this one moved the pointer, and this deploy stopped rather than serve a release the ledger no longer names. Re-run this deploy once the other one has finished if its release should serve",
			promotionID, router.ResolvePointer(pointer), describeActive(active))}}
	}
}

func newStillKept(l projectLedger, pointer, promotionID string) router.StillActive {
	return func(ctx context.Context) error {
		read, err := l.Read(ctx, pointer)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(read.Promotions, func(kept ledger.RecordedPromotion) bool { return kept.PromotionID == promotionID }) {
			return nil
		}
		return refusal.Refuse(refusal.CodeBusy,
			"promotion %s is no longer kept on %s: a prune or rm dropped it while this deploy routed its deployment hostname, so this deploy serves nothing on that hostname",
			promotionID, router.ResolvePointer(pointer))
	}
}

func describeActive(active string) string {
	if active == "" {
		return "nothing"
	}
	return active
}
