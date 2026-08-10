package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cloud/edge"
)

func RollbackTarget(history []edge.HistoryEntry, to, tag string) (edge.Promotion, error) {
	if tag != "" {
		for _, h := range history {
			if h.Tag == tag {
				return h.Promotion, nil
			}
		}
		return edge.Promotion{}, fmt.Errorf("no promotion tagged %q in this project's history", tag)
	}

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

func Rollback(ctx context.Context, stack edge.RootStack, state edge.RootStackState, to, tag string, now int64) (edge.Promotion, error) {
	history, err := stack.History(ctx, state, "")
	if err != nil {
		return edge.Promotion{}, fmt.Errorf("read promotion history: %w", err)
	}
	target, err := RollbackTarget(history, to, tag)
	if err != nil {
		return edge.Promotion{}, err
	}

	promoted := edge.Promotion{PromotionID: target.PromotionID, Ts: now, Builds: target.Builds, Tag: target.Tag}
	if err := stack.Promote(ctx, state, promoted, ""); err != nil {
		return edge.Promotion{}, fmt.Errorf("promote %s: %w", promoted.PromotionID, err)
	}
	return promoted, nil
}
