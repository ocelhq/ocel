package pricing

import (
	"context"

	"github.com/shopspring/decimal"
)

type Catalog interface {
	Lookup(ctx context.Context, queries []Query) ([]Answer, error)
}

type AnswerKind int

const (
	AnswerPriced AnswerKind = iota + 1
	AnswerMissing
	AnswerAmbiguous
	AnswerUnavailable
)

type Answer struct {
	Kind       AnswerKind
	Price      Price
	Reason     string
	Candidates []string
	Matches    int
}

type Price struct {
	SKU            string
	Unit           string
	Steps          []Step
	CatalogVersion string
	Source         string
}

type Step struct {
	Start decimal.Decimal `json:"start"`
	Price decimal.Decimal `json:"price"`
}

type Allowance string

const (
	AllowanceAccount  Allowance = "account"
	AllowanceResource Allowance = "resource"
)

func (p Price) Cost(quantity decimal.Decimal) (cost, marginal decimal.Decimal) {
	return p.Between(decimal.Zero, quantity)
}

func (p Price) Between(from, to decimal.Decimal) (cost, marginal decimal.Decimal) {
	cost = p.cumulative(to).Sub(p.cumulative(from))
	marginal = p.Steps[0].Price
	for _, step := range p.Steps {
		if step.Start.LessThan(to) {
			marginal = step.Price
		}
	}
	return cost, marginal
}

func (p Price) cumulative(quantity decimal.Decimal) decimal.Decimal {
	var cost decimal.Decimal
	for i, step := range p.Steps {
		if quantity.LessThanOrEqual(step.Start) {
			break
		}
		upper := quantity
		if i+1 < len(p.Steps) && p.Steps[i+1].Start.LessThan(quantity) {
			upper = p.Steps[i+1].Start
		}
		cost = cost.Add(upper.Sub(step.Start).Mul(step.Price))
	}
	return cost
}

func (p Price) allows() bool {
	return len(p.Steps) > 1 && p.Steps[0].Price.IsZero()
}
