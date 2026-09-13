package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	host         = "https://pricing.us-east-1.amazonaws.com"
	onDemandTerm = "JRTCKXETXF"
	queryService = "service"
	queryIndex   = "index"
	querySource  = "static"
	globalIndex  = "global"
)

type offer struct {
	Version  string `json:"version"`
	Products map[string]struct {
		SKU        string            `json:"sku"`
		Attributes map[string]string `json:"attributes"`
	} `json:"products"`
	Terms struct {
		OnDemand map[string]map[string]struct {
			OfferTermCode   string `json:"offerTermCode"`
			PriceDimensions map[string]struct {
				BeginRange   string            `json:"beginRange"`
				EndRange     string            `json:"endRange"`
				Unit         string            `json:"unit"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

func main() {
	card := flag.String("card", "platform/aws/provider/cost/rates.json", "the rate card to refresh in place")
	cache := flag.String("cache", filepath.Join(os.TempDir(), "ocel-aws-offers"), "where downloaded offer files are kept")
	flag.Parse()
	if err := run(*card, *cache); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, cache string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var card costkit.Card
	if err := json.Unmarshal(raw, &card); err != nil {
		return err
	}
	offers := map[string]*offer{}
	today := time.Now().UTC().Format(time.DateOnly)
	for i := range card.Rates {
		rate := &card.Rates[i]
		if rate.Query == nil || rate.Query[querySource] != "" {
			continue
		}
		region := rate.Region
		if rate.Query[queryIndex] == globalIndex {
			region = ""
		}
		held, err := load(offers, cache, rate.Query[queryService], region)
		if err != nil {
			return fmt.Errorf("%s: %w", rate.ID, err)
		}
		tiers, unit, source, err := held.tiers(rate.Query, rate.Query[queryService], region)
		if err != nil {
			return fmt.Errorf("%s: %w", rate.ID, err)
		}
		if rate.Unit != unit {
			fmt.Fprintf(os.Stderr, "%s: the offer bills in %q, the card says %q\n", rate.ID, unit, rate.Unit)
		}
		rate.Tiers = tiers
		rate.Source = source
		rate.Verified = today
	}
	card.Version = today
	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func load(offers map[string]*offer, cache, service, region string) (*offer, error) {
	key := service + "/" + region
	if held, ok := offers[key]; ok {
		return held, nil
	}
	path := "/offers/v1.0/aws/" + service + "/current/index.json"
	if region != "" {
		path = "/offers/v1.0/aws/" + service + "/current/" + region + "/index.json"
	}
	file := filepath.Join(cache, service+"-"+orGlobal(region)+".json")
	raw, err := os.ReadFile(file)
	if err != nil {
		req, err := http.NewRequest(http.MethodGet, host+path, nil)
		if err != nil {
			return nil, err
		}
		if raw, err = costkit.Fetch(context.Background(), req); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(cache, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, raw, 0o644); err != nil {
			return nil, err
		}
	}
	held := &offer{}
	if err := json.Unmarshal(raw, held); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	offers[key] = held
	return held, nil
}

func orGlobal(region string) string {
	if region == "" {
		return globalIndex
	}
	return region
}

func (o *offer) tiers(query map[string]string, service, region string) ([]costkit.Tier, string, string, error) {
	var matched []string
	for sku, product := range o.Products {
		if matches(product.Attributes, query, region) {
			matched = append(matched, sku)
		}
	}
	slices.Sort(matched)
	if len(matched) != 1 {
		return nil, "", "", fmt.Errorf("query %v matches %d products: %v", query, len(matched), matched)
	}
	sku := matched[0]
	var tiers []costkit.Tier
	unit := ""
	for _, term := range o.Terms.OnDemand[sku] {
		if term.OfferTermCode != onDemandTerm {
			continue
		}
		for _, dimension := range term.PriceDimensions {
			start, err := decimal.NewFromString(dimension.BeginRange)
			if err != nil {
				return nil, "", "", fmt.Errorf("sku %s: beginRange %q: %w", sku, dimension.BeginRange, err)
			}
			price, err := decimal.NewFromString(dimension.PricePerUnit["USD"])
			if err != nil {
				return nil, "", "", fmt.Errorf("sku %s: price %q: %w", sku, dimension.PricePerUnit["USD"], err)
			}
			tiers = append(tiers, costkit.Tier{Start: start, Price: price})
			unit = dimension.Unit
		}
	}
	if len(tiers) == 0 {
		return nil, "", "", fmt.Errorf("sku %s carries no on-demand term", sku)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Start.LessThan(tiers[j].Start) })
	source := host + "/offers/v1.0/aws/" + service + "/" + o.Version + "/" + orGlobal(region) + "/index.json#" + sku
	return tiers, unit, source, nil
}

func matches(attributes, query map[string]string, region string) bool {
	if region != "" && attributes["regionCode"] != region && attributes["fromRegionCode"] != region {
		return false
	}
	for key, want := range query {
		if key == queryService || key == queryIndex {
			continue
		}
		if attributes[key] != want {
			return false
		}
	}
	return true
}
