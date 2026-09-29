package ledger

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const KeptPromotions = 20

type Pointer struct {
	Name       string              `json:"name"`
	Active     string              `json:"active,omitempty"`
	Sequence   int64               `json:"sequence"`
	Promotions []RecordedPromotion `json:"promotions"`
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
			"point %s at promotion %s: this promote replaces %s there, and %s now names %s, so another deploy moved it while this one was promoting, and this one stopped rather than overwrite it. Re-run it once the other one has finished",
			p.Name, promotion.PromotionID, describePromotion(replaces), p.Name, describePromotion(p.Active))
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
	kept, dropped := next.retain(keep, replaces)
	return kept, dropped, nil
}

func (p Pointer) Retain(keep int) (Pointer, []RecordedPromotion) {
	return p.retain(keep, p.Active)
}

func (p Pointer) retain(keep int, pinned string) (Pointer, []RecordedPromotion) {
	kept := p
	kept.Promotions = nil
	var dropped []RecordedPromotion
	for i, recorded := range p.Promotions {
		if i < keep || recorded.PromotionID == p.Active || recorded.PromotionID == pinned {
			kept.Promotions = append(kept.Promotions, recorded)
			continue
		}
		dropped = append(dropped, recorded)
	}
	return kept, dropped
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
		history = append(history, router.HistoryEntry{Promotion: recorded.Promotion, Active: recorded.PromotionID == p.Active})
	}
	return history
}

func (p Pointer) findFallback(promotionID string) string {
	for at := p.findPromotion(promotionID); at >= 0; {
		displaced := p.Promotions[at].Displaced
		at = p.findPromotion(displaced)
		if at >= 0 && !p.Promotions[at].Unpromoted {
			return displaced
		}
	}
	return ""
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
