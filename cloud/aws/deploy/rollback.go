// Rollback (ADR 0001/0002): re-pointing a production project's
// active-deployment pointer at a prior Promotion. Rollback is itself just
// another Promote call — re-promoting a past Promotion's builds under a fresh
// timestamp so it becomes the newest history entry — which is what makes the
// rolled-back state itself roll-forward-able: the Promotion that was active
// before the rollback is now the "immediately previous" one.
package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cloud/edge"
)

// RollbackTarget selects, from a project's promotion history (newest-first,
// exactly what edge.RootTier.History returns), the Promotion rollback should
// re-promote: the one named by to when it is non-empty, else the one
// immediately after the currently active entry (the "previous" Promotion).
// Pure.
func RollbackTarget(history []edge.HistoryEntry, to string) (edge.Promotion, error) {
	if to != "" {
		for _, h := range history {
			if h.PromotionID == to {
				return h.Promotion, nil
			}
		}
		return edge.Promotion{}, fmt.Errorf("no promotion %q in this project's history", to)
	}

	activeIdx := -1
	for i, h := range history {
		if h.Active {
			activeIdx = i
			break
		}
	}
	if activeIdx == -1 {
		return edge.Promotion{}, fmt.Errorf("this project has no active promotion to roll back from")
	}
	if activeIdx+1 >= len(history) {
		return edge.Promotion{}, fmt.Errorf("this project has no earlier promotion to roll back to")
	}
	return history[activeIdx+1].Promotion, nil
}

// Rollback re-points a project's active-deployment pointer at a prior
// Promotion: RollbackTarget selects it from the store's current history
// (immediately previous when to is empty, else the one named by to), then a
// fresh Promote call — carrying the target's builds under a new timestamp —
// makes it the newest history entry and the active one. Pure of AWS: only
// edge.RootTier is called, and it is exercised directly against the
// edge.RootTier fake in tests.
func Rollback(ctx context.Context, tier edge.RootTier, state edge.RootTierState, to string, now int64) (edge.Promotion, error) {
	history, err := tier.History(ctx, state)
	if err != nil {
		return edge.Promotion{}, fmt.Errorf("read promotion history: %w", err)
	}
	target, err := RollbackTarget(history, to)
	if err != nil {
		return edge.Promotion{}, err
	}

	promoted := edge.Promotion{PromotionID: target.PromotionID, Ts: now, Builds: target.Builds}
	if err := tier.Promote(ctx, state, promoted); err != nil {
		return edge.Promotion{}, fmt.Errorf("promote %s: %w", promoted.PromotionID, err)
	}
	return promoted, nil
}
