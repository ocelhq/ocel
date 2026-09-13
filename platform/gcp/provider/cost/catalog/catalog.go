package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	Host         = "https://cloudbilling.googleapis.com/v1"
	KeyVariable  = "GCP_BILLING_API_KEY"
	KeyHeader    = "x-goog-api-key"
	GlobalRegion = "global"

	pageSize     = 5000
	nanosPerUnit = 1_000_000_000
)

type SKU struct {
	SKUID          string            `json:"skuId"`
	Description    string            `json:"description"`
	ServiceRegions []string          `json:"serviceRegions"`
	Category       map[string]string `json:"category"`
	PricingInfo    []struct {
		PricingExpression struct {
			UsageUnit   string `json:"usageUnit"`
			TieredRates []struct {
				StartUsageAmount float64 `json:"startUsageAmount"`
				UnitPrice        struct {
					Units string `json:"units"`
					Nanos int64  `json:"nanos"`
				} `json:"unitPrice"`
			} `json:"tieredRates"`
		} `json:"pricingExpression"`
	} `json:"pricingInfo"`
}

func List(ctx context.Context, host, service, key string) ([]SKU, error) {
	var skus []SKU
	token := ""
	for {
		endpoint := fmt.Sprintf("%s/services/%s/skus?pageSize=%d", host, service, pageSize)
		if token != "" {
			endpoint += "&pageToken=" + url.QueryEscape(token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set(KeyHeader, key)
		body, err := costkit.Fetch(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("list skus of %s: %w", service, err)
		}
		var page struct {
			SKUs          []SKU  `json:"skus"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		skus = append(skus, page.SKUs...)
		if page.NextPageToken == "" {
			return skus, nil
		}
		token = page.NextPageToken
	}
}

func Match(skus []SKU, description, region string) []SKU {
	if region == "" {
		region = GlobalRegion
	}
	var matched []SKU
	for _, sku := range skus {
		if !strings.Contains(sku.Description, description) {
			continue
		}
		if !slices.Contains(sku.ServiceRegions, region) {
			continue
		}
		matched = append(matched, sku)
	}
	return matched
}

func Tiers(sku SKU) ([]costkit.Tier, string, error) {
	if len(sku.PricingInfo) == 0 {
		return nil, "", fmt.Errorf("sku %s carries no pricing", sku.SKUID)
	}
	expression := sku.PricingInfo[0].PricingExpression
	var tiers []costkit.Tier
	for _, tier := range expression.TieredRates {
		units, err := decimal.NewFromString(tier.UnitPrice.Units)
		if err != nil {
			return nil, "", fmt.Errorf("sku %s: units %q: %w", sku.SKUID, tier.UnitPrice.Units, err)
		}
		price := units.Add(decimal.NewFromInt(tier.UnitPrice.Nanos).Div(decimal.NewFromInt(nanosPerUnit)))
		tiers = append(tiers, costkit.Tier{Start: decimal.NewFromFloat(tier.StartUsageAmount), Price: price})
	}
	if len(tiers) == 0 {
		return nil, "", fmt.Errorf("sku %s carries no tiered rates", sku.SKUID)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Start.LessThan(tiers[j].Start) })
	return tiers, expression.UsageUnit, nil
}

func Source(sku SKU) string {
	return "https://cloud.google.com/skus?filter=" + url.QueryEscape(sku.SKUID)
}
