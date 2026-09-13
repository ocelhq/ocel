package costkit

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

type Card struct {
	Version  string `json:"version"`
	Currency string `json:"currency"`
	Rates    []Rate `json:"rates"`

	index map[rateKey]int
}

type Rate struct {
	ID       string            `json:"id"`
	Region   string            `json:"region,omitempty"`
	Unit     string            `json:"unit"`
	Tiers    []Tier            `json:"tiers"`
	Source   string            `json:"source"`
	Verified string            `json:"verified"`
	Query    map[string]string `json:"query,omitempty"`
}

type Tier struct {
	Start decimal.Decimal `json:"start"`
	Price decimal.Decimal `json:"price"`
}

type rateKey struct{ id, region string }

func Load(raw []byte) (*Card, error) {
	var card Card
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("rate card: %w", err)
	}
	if card.Version == "" || card.Currency == "" {
		return nil, fmt.Errorf("rate card: a card names its version and currency")
	}
	card.index = make(map[rateKey]int, len(card.Rates))
	for i, rate := range card.Rates {
		if err := rate.check(); err != nil {
			return nil, fmt.Errorf("rate card: %w", err)
		}
		key := rateKey{rate.ID, rate.Region}
		if _, dup := card.index[key]; dup {
			return nil, fmt.Errorf("rate card: rate %s in region %q appears twice", rate.ID, rate.Region)
		}
		card.index[key] = i
	}
	return &card, nil
}

func (r Rate) check() error {
	switch {
	case r.ID == "":
		return fmt.Errorf("a rate has no id")
	case r.Unit == "":
		return fmt.Errorf("rate %s names no unit", r.ID)
	case r.Source == "" || r.Verified == "":
		return fmt.Errorf("rate %s carries no source and verified date", r.ID)
	case len(r.Tiers) == 0:
		return fmt.Errorf("rate %s has no tiers", r.ID)
	}
	if !r.Tiers[0].Start.IsZero() {
		return fmt.Errorf("rate %s: the first tier starts at %s, not 0", r.ID, r.Tiers[0].Start)
	}
	if !sort.SliceIsSorted(r.Tiers, func(i, j int) bool { return r.Tiers[i].Start.LessThan(r.Tiers[j].Start) }) {
		return fmt.Errorf("rate %s: tiers are not in ascending order", r.ID)
	}
	return nil
}

func (c *Card) Lookup(id, region string) (Rate, bool) {
	if i, ok := c.index[rateKey{id, region}]; ok {
		return c.Rates[i], true
	}
	if i, ok := c.index[rateKey{id, ""}]; ok {
		return c.Rates[i], true
	}
	return Rate{}, false
}

func (r Rate) Cost(quantity decimal.Decimal) (cost, marginal decimal.Decimal) {
	for i, tier := range r.Tiers {
		if quantity.LessThanOrEqual(tier.Start) {
			break
		}
		upper := quantity
		if i+1 < len(r.Tiers) && r.Tiers[i+1].Start.LessThan(quantity) {
			upper = r.Tiers[i+1].Start
		}
		cost = cost.Add(upper.Sub(tier.Start).Mul(tier.Price))
		marginal = tier.Price
	}
	if quantity.IsZero() {
		marginal = r.Tiers[0].Price
	}
	return cost, marginal
}

func Merge(primary *Card, others ...*Card) *Card {
	merged := &Card{Version: primary.Version, Currency: primary.Currency, index: map[rateKey]int{}}
	for _, card := range append([]*Card{primary}, others...) {
		for _, rate := range card.Rates {
			key := rateKey{rate.ID, rate.Region}
			if _, held := merged.index[key]; held {
				continue
			}
			merged.index[key] = len(merged.Rates)
			merged.Rates = append(merged.Rates, rate)
		}
	}
	return merged
}
