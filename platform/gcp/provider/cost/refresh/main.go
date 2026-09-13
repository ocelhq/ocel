package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost/catalog"
)

func main() {
	card := flag.String("card", "platform/gcp/provider/cost/rates.json", "the rate card to refresh in place")
	flag.Parse()
	key := os.Getenv(catalog.KeyVariable)
	if key == "" {
		fmt.Fprintf(os.Stderr, "%s names no API key, and the Cloud Billing Catalog answers registered callers only\n", catalog.KeyVariable)
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
	held := map[string][]catalog.SKU{}
	today := time.Now().UTC().Format(time.DateOnly)
	for i := range card.Rates {
		rate := &card.Rates[i]
		if rate.Query == nil {
			continue
		}
		service := rate.Query[costkit.QueryService]
		skus, listed := held[service]
		if !listed {
			if skus, err = catalog.List(context.Background(), catalog.Host, service, key); err != nil {
				return fmt.Errorf("%s: %w", rate.ID, err)
			}
			held[service] = skus
		}
		matched := catalog.Match(skus, rate.Query[costkit.QueryDescription], rate.Query[costkit.QueryRegion])
		if len(matched) != 1 {
			names := make([]string, 0, len(matched))
			for _, sku := range matched {
				names = append(names, sku.Description)
			}
			return fmt.Errorf("%s: query %v matches %d skus: %v", rate.ID, rate.Query, len(matched), names)
		}
		tiers, unit, err := catalog.Tiers(matched[0])
		if err != nil {
			return fmt.Errorf("%s: %w", rate.ID, err)
		}
		if rate.Unit != unit {
			fmt.Fprintf(os.Stderr, "%s: the catalog bills in %q, the card says %q\n", rate.ID, unit, rate.Unit)
		}
		rate.Tiers = tiers
		rate.Source = catalog.Source(matched[0])
		rate.Verified = today
	}
	card.Version = today
	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
