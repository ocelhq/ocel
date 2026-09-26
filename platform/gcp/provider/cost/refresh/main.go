package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	host             = "https://cloudbilling.googleapis.com/v1"
	keyVariable      = "GCP_BILLING_API_KEY"
	keyHeader        = "x-goog-api-key"
	queryService     = "service"
	queryDescription = "description"
	queryRegion      = "region"
	globalRegion     = "global"
	nanosPerUnit     = 1_000_000_000
)

type sku struct {
	SKUID          string   `json:"skuId"`
	Description    string   `json:"description"`
	ServiceRegions []string `json:"serviceRegions"`
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

func main() {
	card := flag.String("card", "platform/gcp/provider/cost/rates.json", "the rate card to refresh in place")
	flag.Parse()
	key := os.Getenv(keyVariable)
	if key == "" {
		fmt.Fprintf(os.Stderr, "%s names no API key, and the Cloud Billing Catalog answers registered callers only\n", keyVariable)
		os.Exit(1)
	}
	if err := run(*card, key); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, key string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var card costkit.Card
	if err := json.Unmarshal(raw, &card); err != nil {
		return err
	}
	catalog := map[string][]sku{}
	today := time.Now().UTC().Format(time.DateOnly)
	for i := range card.Rates {
		rate := &card.Rates[i]
		if rate.Query == nil {
			continue
		}
		service := rate.Query[queryService]
		skus, listed := catalog[service]
		if !listed {
			if skus, err = list(service, key); err != nil {
				return fmt.Errorf("%s: %w", rate.ID, err)
			}
			catalog[service] = skus
		}
		matched := match(skus, rate.Query)
		if len(matched) != 1 {
			names := make([]string, 0, len(matched))
			for _, s := range matched {
				names = append(names, s.Description)
			}
			return fmt.Errorf("%s: query %v matches %d skus: %v", rate.ID, rate.Query, len(matched), names)
		}
		steps, unit, err := stepsOf(matched[0])
		if err != nil {
			return fmt.Errorf("%s: %w", rate.ID, err)
		}
		if rate.Unit != unit {
			fmt.Fprintf(os.Stderr, "%s: the catalog bills in %q, the card says %q\n", rate.ID, unit, rate.Unit)
		}
		rate.Steps = steps
		rate.Source = "https://cloud.google.com/skus?filter=" + url.QueryEscape(matched[0].SKUID)
		rate.Verified = today
	}
	card.Version = today
	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func list(service, key string) ([]sku, error) {
	var skus []sku
	token := ""
	for {
		endpoint := host + "/services/" + service + "/skus?pageSize=5000"
		if token != "" {
			endpoint += "&pageToken=" + url.QueryEscape(token)
		}
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set(keyHeader, key)
		body, err := costkit.Fetch(context.Background(), req)
		if err != nil {
			return nil, fmt.Errorf("list skus of %s: %w", service, err)
		}
		var page struct {
			SKUs          []sku  `json:"skus"`
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

func match(skus []sku, query map[string]string) []sku {
	region := query[queryRegion]
	if region == "" {
		region = globalRegion
	}
	var matched []sku
	for _, s := range skus {
		if !strings.Contains(s.Description, query[queryDescription]) {
			continue
		}
		if !slices.Contains(s.ServiceRegions, region) {
			continue
		}
		matched = append(matched, s)
	}
	return matched
}

func stepsOf(s sku) ([]costkit.PriceStep, string, error) {
	if len(s.PricingInfo) == 0 {
		return nil, "", fmt.Errorf("sku %s has no pricing", s.SKUID)
	}
	expression := s.PricingInfo[0].PricingExpression
	var steps []costkit.PriceStep
	for _, tier := range expression.TieredRates {
		units, err := decimal.NewFromString(tier.UnitPrice.Units)
		if err != nil {
			return nil, "", fmt.Errorf("sku %s: units %q: %w", s.SKUID, tier.UnitPrice.Units, err)
		}
		price := units.Add(decimal.NewFromInt(tier.UnitPrice.Nanos).Div(decimal.NewFromInt(nanosPerUnit)))
		steps = append(steps, costkit.PriceStep{Start: decimal.NewFromFloat(tier.StartUsageAmount), Price: price})
	}
	if len(steps) == 0 {
		return nil, "", fmt.Errorf("sku %s has no tiered rates", s.SKUID)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Start.LessThan(steps[j].Start) })
	return steps, expression.UsageUnit, nil
}
