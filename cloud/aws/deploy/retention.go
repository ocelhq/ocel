package deploy

// HistoryEntry is one promotion in the deployments store's ordered history,
// annotated with whether it is the currently active one. Mirrors
// HistoryEntry in workers/deployments-store/src/store.ts, whose history()
// returns entries newest-first — Retention expects the same order.
type HistoryEntry struct {
	Promotion
	Active bool
}

// Retention applies a `--keep N` window to a promotion history: keep holds
// the promotion ids prune must pin, collect holds the ones it may reclaim.
// The active promotion is always kept even if it falls outside the N-deep
// window, so pruning can never take the live site down — the same rule the
// deployments store's own prune() enforces over its history. history must be
// newest-first; both returned slices preserve that order. A non-positive
// keepN keeps only the active promotion. Pure.
func Retention(history []HistoryEntry, keepN int) (keep, collect []string) {
	if keepN < 0 {
		keepN = 0
	}
	for i, h := range history {
		if i < keepN || h.Active {
			keep = append(keep, h.PromotionID)
		} else {
			collect = append(collect, h.PromotionID)
		}
	}
	return keep, collect
}
