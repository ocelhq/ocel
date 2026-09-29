package ledger

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const KeptPromotions = 20

type Pointer struct {
	Name    string  `json:"name"`
	Active  string  `json:"active,omitempty"`
	Seq     int64   `json:"seq"`
	Entries []Entry `json:"entries"`
}

type Entry struct {
	router.Promotion
	Seq        int64  `json:"seq"`
	Displaced  string `json:"displaced,omitempty"`
	Unpromoted bool   `json:"unpromoted,omitempty"`
}

func (p Pointer) Promote(promotion router.Promotion, over string, keep int) (Pointer, []Entry, error) {
	if p.Active != over {
		return Pointer{}, nil, refusal.Refuse(refusal.CodeBusy,
			"point %s at promotion %s: this promote replaces %s there, and %s now names %s, so another deploy moved it while this one was promoting, and this one stopped rather than overwrite it. Re-run it once the other one has finished",
			p.Name, promotion.PromotionID, describePromotion(over), p.Name, describePromotion(p.Active))
	}
	for _, entry := range p.Entries {
		if entry.PromotionID == promotion.PromotionID {
			return Pointer{}, nil, refusal.Refuse(refusal.CodeInvalid,
				"promote %s: %s already records a promotion by that id, and a promotion is recorded once",
				promotion.PromotionID, p.Name)
		}
		if promotion.Tag != "" && entry.Tag == promotion.Tag {
			return Pointer{}, nil, refusal.Refuse(refusal.CodeInvalid,
				"promote %s: the tag %q already names promotion %s on %s, and a tag names one release so that `ocel rollback --tag %s` is unambiguous. Pick another tag, or roll back to %s instead",
				promotion.PromotionID, promotion.Tag, entry.PromotionID, p.Name, promotion.Tag, entry.PromotionID)
		}
	}
	next := p
	next.Seq++
	next.Active = promotion.PromotionID
	next.Entries = append([]Entry{{Promotion: promotion, Seq: next.Seq, Displaced: over}}, slices.Clone(p.Entries)...)
	kept, dropped := next.retain(keep, over)
	return kept, dropped, nil
}

func (p Pointer) Retain(keep int) (Pointer, []Entry) {
	return p.retain(keep, p.Active)
}

func (p Pointer) retain(keep int, pinned string) (Pointer, []Entry) {
	kept := p
	kept.Entries = nil
	var dropped []Entry
	for i, entry := range p.Entries {
		if i < keep || entry.PromotionID == p.Active || entry.PromotionID == pinned {
			kept.Entries = append(kept.Entries, entry)
			continue
		}
		dropped = append(dropped, entry)
	}
	return kept, dropped
}

func (p Pointer) Unpromote(promotionID string) (Pointer, error) {
	at := p.findEntry(promotionID)
	if at < 0 {
		return Pointer{}, fmt.Errorf("take back promotion %s: %s records no such promotion", promotionID, p.Name)
	}
	next := p
	next.Entries = slices.Clone(p.Entries)
	next.Entries[at].Unpromoted = true
	next.Entries[at].Tag = ""
	if next.Active == promotionID {
		next.Active = next.findFallback(promotionID)
	}
	return next, nil
}

func (p Pointer) History() []router.HistoryEntry {
	history := make([]router.HistoryEntry, 0, len(p.Entries))
	for _, entry := range p.Entries {
		history = append(history, router.HistoryEntry{Promotion: entry.Promotion, Active: entry.PromotionID == p.Active})
	}
	return history
}

func (p Pointer) findFallback(promotionID string) string {
	for at := p.findEntry(promotionID); at >= 0; {
		displaced := p.Entries[at].Displaced
		at = p.findEntry(displaced)
		if at >= 0 && !p.Entries[at].Unpromoted {
			return displaced
		}
	}
	return ""
}

func (p Pointer) findEntry(promotionID string) int {
	if promotionID == "" {
		return -1
	}
	return slices.IndexFunc(p.Entries, func(entry Entry) bool { return entry.PromotionID == promotionID })
}

func describePromotion(promotionID string) string {
	if promotionID == "" {
		return "nothing"
	}
	return "promotion " + promotionID
}
