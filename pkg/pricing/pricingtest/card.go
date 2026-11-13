package pricingtest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/ingest"
)

type Card struct {
	products []product

	mu      sync.Mutex
	lookups [][]pricing.Query
}

type product struct {
	ingest.StaticProduct
	vendor  string
	version string
	steps   []pricing.Step
}

func NewCard(catalogs ...[]byte) (*Card, error) {
	card := &Card{}
	for _, raw := range catalogs {
		catalog, err := ingest.ReadStatic(raw)
		if err != nil {
			return nil, fmt.Errorf("test card: %w", err)
		}
		for _, p := range catalog.Products {
			steps := make([]pricing.Step, 0, len(p.Tiers))
			for _, tier := range p.Tiers {
				steps = append(steps, pricing.Step{Start: decimal.RequireFromString(tier.Start), Price: decimal.RequireFromString(tier.Price)})
			}
			card.products = append(card.products, product{StaticProduct: p, vendor: catalog.Vendor, version: catalog.Version, steps: steps})
		}
	}
	return card, nil
}

func (c *Card) Lookup(_ context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	c.mu.Lock()
	c.lookups = append(c.lookups, slices.Clone(queries))
	c.mu.Unlock()
	answers := make([]pricing.Answer, len(queries))
	for i, query := range queries {
		answers[i] = c.answer(query)
	}
	return answers, nil
}

func (c *Card) Lookups() [][]pricing.Query {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lookups)
}

func (c *Card) answer(query pricing.Query) pricing.Answer {
	seeded := false
	var matched []product
	for _, p := range c.products {
		if p.vendor != query.Vendor || p.Service != query.Service {
			continue
		}
		seeded = true
		if p.matches(query) {
			matched = append(matched, p)
		}
	}
	switch {
	case !seeded:
		return pricing.Answer{Kind: pricing.AnswerMissing, Reason: fmt.Sprintf("no %s catalog for %s is seeded", query.Vendor, query.Service)}
	case len(matched) == 0:
		return pricing.Answer{Kind: pricing.AnswerMissing, Reason: "no product matches"}
	case len(matched) > 1:
		skus := make([]string, 0, len(matched))
		for _, p := range matched {
			skus = append(skus, p.SKU)
		}
		slices.Sort(skus)
		return pricing.Answer{Kind: pricing.AnswerAmbiguous, Candidates: skus, Matches: len(skus)}
	}
	p := matched[0]
	return pricing.Answer{Kind: pricing.AnswerPriced, Price: pricing.Price{
		SKU: p.SKU, Unit: p.Unit, Steps: p.steps, CatalogVersion: p.version, Source: p.Source,
	}}
}

func (p product) matches(query pricing.Query) bool {
	if query.PurchaseOption != "" && string(query.PurchaseOption) != p.PurchaseOption {
		return false
	}
	if p.Region != query.Region {
		return false
	}
	if query.ProductFamily != "" && p.ProductFamily != query.ProductFamily {
		return false
	}
	if query.Unit != "" && p.Unit != query.Unit {
		return false
	}
	for key, value := range query.Attributes {
		if got, ok := p.Attributes[key]; !ok || got != value {
			return false
		}
	}
	return true
}
