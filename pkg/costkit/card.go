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
	ID        string            `json:"id"`
	Region    string            `json:"region,omitempty"`
	Unit      string            `json:"unit"`
	Steps     []PriceStep       `json:"steps"`
	Allowance Allowance         `json:"allowance,omitempty"`
	Note      string            `json:"note,omitempty"`
	Source    string            `json:"source"`
	Verified  string            `json:"verified"`
	Query     map[string]string `json:"query,omitempty"`
}

type Allowance string

const (
	AllowanceAccount  Allowance = "account"
	AllowanceResource Allowance = "resource"
)

type PriceStep struct {
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
		return fmt.Errorf("rate %s has no source and verified date", r.ID)
	case len(r.Steps) == 0:
		return fmt.Errorf("rate %s has no price steps", r.ID)
	}
	if !r.Steps[0].Start.IsZero() {
		return fmt.Errorf("rate %s: the first step starts at %s, not 0", r.ID, r.Steps[0].Start)
	}
	if !sort.SliceIsSorted(r.Steps, func(i, j int) bool { return r.Steps[i].Start.LessThan(r.Steps[j].Start) }) {
		return fmt.Errorf("rate %s: its price steps are not in ascending order", r.ID)
	}
	switch r.Allowance {
	case "":
		if r.allows() {
			return fmt.Errorf("rate %s folds a free allowance into its first step and does not say whether the account or each resource gets it", r.ID)
		}
	case AllowanceAccount, AllowanceResource:
		if !r.allows() {
			return fmt.Errorf("rate %s scopes an allowance and its first step is not free", r.ID)
		}
	default:
		return fmt.Errorf("rate %s: an allowance is %q or %q, not %q", r.ID, AllowanceAccount, AllowanceResource, r.Allowance)
	}
	return nil
}

func (r Rate) allows() bool {
	return len(r.Steps) > 1 && r.Steps[0].Price.IsZero()
}

func (c *Card) Lookup(id, region string) (rate Rate, found, fellBack bool) {
	if i, ok := c.index[rateKey{id, region}]; ok {
		return c.Rates[i], true, false
	}
	if i, ok := c.index[rateKey{id, ""}]; ok {
		return c.Rates[i], true, region != ""
	}
	return Rate{}, false, false
}

func (r Rate) Cost(quantity decimal.Decimal) (cost, marginal decimal.Decimal) {
	return r.Between(decimal.Zero, quantity)
}

func (r Rate) Between(from, to decimal.Decimal) (cost, marginal decimal.Decimal) {
	cost = r.cumulative(to).Sub(r.cumulative(from))
	marginal = r.Steps[0].Price
	for _, step := range r.Steps {
		if step.Start.LessThan(to) {
			marginal = step.Price
		}
	}
	return cost, marginal
}

func (r Rate) cumulative(quantity decimal.Decimal) decimal.Decimal {
	var cost decimal.Decimal
	for i, step := range r.Steps {
		if quantity.LessThanOrEqual(step.Start) {
			break
		}
		upper := quantity
		if i+1 < len(r.Steps) && r.Steps[i+1].Start.LessThan(quantity) {
			upper = r.Steps[i+1].Start
		}
		cost = cost.Add(upper.Sub(step.Start).Mul(step.Price))
	}
	return cost
}

func Merge(primary *Card, others ...*Card) *Card {
	merged := &Card{Version: primary.Version, Currency: primary.Currency, index: map[rateKey]int{}}
	for _, card := range append([]*Card{primary}, others...) {
		for _, rate := range card.Rates {
			key := rateKey{rate.ID, rate.Region}
			if _, ok := merged.index[key]; ok {
				continue
			}
			merged.index[key] = len(merged.Rates)
			merged.Rates = append(merged.Rates, rate)
		}
	}
	return merged
}
