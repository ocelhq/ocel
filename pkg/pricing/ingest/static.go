package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

const PurchaseOnDemand = "on_demand"

type StaticCatalog struct {
	Format   int             `json:"format"`
	Vendor   string          `json:"vendor"`
	Version  string          `json:"version"`
	Currency string          `json:"currency"`
	Products []StaticProduct `json:"products"`
}

type StaticProduct struct {
	Service        string            `json:"service"`
	SKU            string            `json:"sku"`
	Region         string            `json:"region"`
	ProductFamily  string            `json:"productFamily"`
	Attributes     map[string]string `json:"attributes"`
	PurchaseOption string            `json:"purchaseOption"`
	Unit           string            `json:"unit"`
	Tiers          []Tier            `json:"tiers"`
	Source         string            `json:"source"`
	Verified       string            `json:"verified"`
}

type Tier struct {
	Start string `json:"start"`
	Price string `json:"price"`
}

func ReadStatic(raw []byte) (StaticCatalog, error) {
	var catalog StaticCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return StaticCatalog{}, fmt.Errorf("static catalog: %w", err)
	}
	if err := catalog.refuseInvalid(); err != nil {
		return StaticCatalog{}, fmt.Errorf("static catalog %s: %w", catalog.Vendor, err)
	}
	return catalog, nil
}

type productKey struct{ service, sku, region, purchaseOption string }

func (c StaticCatalog) refuseInvalid() error {
	switch {
	case c.Format != Format:
		return fmt.Errorf("format is %d, not %d", c.Format, Format)
	case c.Currency != Currency:
		return fmt.Errorf("currency is %q, not %s", c.Currency, Currency)
	case c.Vendor == "" || c.Version == "":
		return errors.New("a catalog names its vendor and version")
	}
	seen := map[productKey]bool{}
	for _, product := range c.Products {
		key := productKey{product.Service, product.SKU, product.Region, product.PurchaseOption}
		if seen[key] {
			return fmt.Errorf("sku %s appears twice in %s %q", product.SKU, product.Service, product.Region)
		}
		seen[key] = true
		if err := product.refuseInvalid(); err != nil {
			return fmt.Errorf("sku %q: %w", product.SKU, err)
		}
	}
	return nil
}

func (p StaticProduct) refuseInvalid() error {
	switch {
	case p.Service == "" || p.SKU == "" || p.Unit == "":
		return errors.New("a product names its service, sku and unit")
	case p.PurchaseOption != PurchaseOnDemand:
		return fmt.Errorf("purchase option is %q, not %s", p.PurchaseOption, PurchaseOnDemand)
	case p.Source == "" || p.Verified == "":
		return errors.New("a product names its source and the date it was verified")
	case len(p.Tiers) == 0:
		return errors.New("a product has at least one tier")
	}
	previous := decimal.Zero
	for i, tier := range p.Tiers {
		start, err := decimal.NewFromString(tier.Start)
		if err != nil {
			return fmt.Errorf("tier %d starts at %q, not a decimal", i, tier.Start)
		}
		price, err := decimal.NewFromString(tier.Price)
		if err != nil || price.IsNegative() {
			return fmt.Errorf("tier %d costs %q, not a decimal of 0 or more", i, tier.Price)
		}
		switch {
		case i == 0 && !start.IsZero():
			return fmt.Errorf("the first tier starts at %s, not 0", tier.Start)
		case i > 0 && !start.GreaterThan(previous):
			return fmt.Errorf("tier %d starts at %s, not after %s", i, tier.Start, previous)
		}
		previous = start
	}
	return nil
}
