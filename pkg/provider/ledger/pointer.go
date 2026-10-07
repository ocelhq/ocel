package ledger

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const KeptPromotions = 20

type Pointer struct {
	Name            string              `json:"name"`
	Active          string              `json:"active,omitempty"`
	Sequence        int64               `json:"sequence"`
	Promotions      []RecordedPromotion `json:"promotions"`
	PendingRemovals []RecordedPromotion `json:"pendingRemovals,omitempty"`
}

func (p Pointer) ListDeploymentRemovals() []router.PointerRemoval {
	return collectDeploymentRemovals(p.Name, append(slices.Clone(p.Promotions), p.PendingRemovals...))
}

type RecordedPromotion struct {
	router.Promotion
	Sequence   int64  `json:"sequence"`
	Displaced  string `json:"displaced,omitempty"`
	Unpromoted bool   `json:"unpromoted,omitempty"`
}

func (p Pointer) Promote(promotion router.Promotion, replaces string, keep int) (Pointer, []RecordedPromotion, error) {
	if p.Active != replaces {
		return Pointer{}, nil, refusal.Refuse(refusal.CodeBusy,
			"promote %s on %s: %s now names %s, not %s, because another deploy or rollback promoted it after this one started. This one stopped without moving anything rather than replace a release it never saw. Run `ocel deploy` (or `ocel rollback`) again to promote over it",
			promotion.PromotionID, p.Name, p.Name, describePromotion(p.Active), describePromotion(replaces))
	}
	for _, recorded := range p.Promotions {
		if recorded.PromotionID == promotion.PromotionID {
			return Pointer{}, nil, refusal.Refuse(refusal.CodeInvalid,
				"promote %s: %s already records a promotion by that id, and a promotion is recorded once",
				promotion.PromotionID, p.Name)
		}
		if promotion.Tag != "" && recorded.Tag == promotion.Tag {
			return Pointer{}, nil, refusal.Refuse(refusal.CodeInvalid,
				"promote %s: the tag %q already names promotion %s on %s, and a tag names one release so that `ocel rollback --tag %s` is unambiguous. Pick another tag, or roll back to %s instead",
				promotion.PromotionID, promotion.Tag, recorded.PromotionID, p.Name, promotion.Tag, recorded.PromotionID)
		}
	}
	next := p
	next.Sequence++
	next.Active = promotion.PromotionID
	next.Promotions = append([]RecordedPromotion{{Promotion: promotion, Sequence: next.Sequence, Displaced: replaces}}, slices.Clone(p.Promotions)...)
	kept, dropped := next.Retain(keep)
	return kept, dropped, nil
}

func (p Pointer) Rollback(target string, promotion router.Promotion, replaces string, keep int) (Pointer, []RecordedPromotion, error) {
	at := p.findPromotion(target)
	if at < 0 {
		return Pointer{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"roll %s back to promotion %s: %s no longer records it, because a prune or a promote dropped it while this rollback ran. `ocel deployments ls` lists the promotions a rollback can reach",
			p.Name, target, p.Name)
	}
	if p.Promotions[at].Unpromoted {
		return Pointer{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"roll %s back to promotion %s: it was taken back when a router could not serve it, so it never served. Roll back to a promotion `ocel deployments ls` does not mark unpromoted",
			p.Name, target)
	}
	return p.Promote(promotion, replaces, keep)
}

func (p Pointer) Retain(keep int) (Pointer, []RecordedPromotion) {
	pinned := append([]string{p.Active}, p.findFallbackChain(p.Active)...)
	kept := p
	kept.Promotions = nil
	var dropped []RecordedPromotion
	for i, recorded := range p.Promotions {
		if i < keep || slices.Contains(pinned, recorded.PromotionID) {
			kept.Promotions = append(kept.Promotions, recorded)
			continue
		}
		dropped = append(dropped, recorded)
	}
	kept.PendingRemovals = slices.Clone(p.PendingRemovals)
	for _, recorded := range dropped {
		if len(recorded.Hosts) > 0 {
			kept.PendingRemovals = append(kept.PendingRemovals, recorded)
		}
	}
	return kept, dropped
}

func (p Pointer) ForgetPendingRemovals(withdrawn []string) Pointer {
	next := p
	next.PendingRemovals = slices.DeleteFunc(slices.Clone(p.PendingRemovals), func(pending RecordedPromotion) bool {
		return slices.Contains(withdrawn, router.FormatDeploymentPointer(p.Name, pending.PromotionID))
	})
	return next
}

func (p Pointer) Unpromote(promotionID string) (Pointer, error) {
	at := p.findPromotion(promotionID)
	if at < 0 {
		return Pointer{}, fmt.Errorf("take back promotion %s: %s records no such promotion", promotionID, p.Name)
	}
	next := p
	next.Promotions = slices.Clone(p.Promotions)
	next.Promotions[at].Unpromoted = true
	next.Promotions[at].Tag = ""
	if next.Active == promotionID {
		next.Active = next.findFallback(promotionID)
	}
	return next, nil
}

func (p Pointer) History() []router.HistoryEntry {
	history := make([]router.HistoryEntry, 0, len(p.Promotions))
	for _, recorded := range p.Promotions {
		history = append(history, router.HistoryEntry{
			Promotion:  recorded.Promotion,
			Active:     recorded.PromotionID == p.Active,
			Unpromoted: recorded.Unpromoted,
		})
	}
	return history
}

func (p Pointer) findFallback(promotionID string) string {
	chain := p.findFallbackChain(promotionID)
	if len(chain) == 0 {
		return ""
	}
	fallback := chain[len(chain)-1]
	if p.Promotions[p.findPromotion(fallback)].Unpromoted {
		return ""
	}
	return fallback
}

func (p Pointer) findFallbackChain(promotionID string) []string {
	var chain []string
	for at := p.findPromotion(promotionID); at >= 0; {
		displaced := p.Promotions[at].Displaced
		if at = p.findPromotion(displaced); at < 0 {
			break
		}
		chain = append(chain, displaced)
		if !p.Promotions[at].Unpromoted {
			break
		}
	}
	return chain
}

func (p Pointer) findPromotion(promotionID string) int {
	if promotionID == "" {
		return -1
	}
	return slices.IndexFunc(p.Promotions, func(recorded RecordedPromotion) bool { return recorded.PromotionID == promotionID })
}

func describePromotion(promotionID string) string {
	if promotionID == "" {
		return "nothing"
	}
	return "promotion " + promotionID
}
