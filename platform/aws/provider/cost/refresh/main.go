package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/platform/aws/provider/cost/offer"
)

type group struct{ service, region string }

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
	groups := map[group][]*costkit.Rate{}
	for i := range card.Rates {
		rate := &card.Rates[i]
		if rate.Query == nil || rate.Query[costkit.QuerySource] != "" {
			continue
		}
		region := rate.Region
		if rate.Query[costkit.QueryIndex] == costkit.Global {
			region = ""
		}
		held := group{rate.Query[costkit.QueryService], region}
		groups[held] = append(groups[held], rate)
	}
	order := make([]group, 0, len(groups))
	for held := range groups {
		order = append(order, held)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].service != order[j].service {
			return order[i].service < order[j].service
		}
		return order[i].region < order[j].region
	})
	today := time.Now().UTC().Format(time.DateOnly)
	for _, held := range order {
		file, err := ensure(cache, held.service, held.region)
		if err != nil {
			return err
		}
		if err := resolve(file, groups[held], held, today); err != nil {
			return err
		}
	}
	card.Version = today
	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func ensure(cache, service, region string) (string, error) {
	file := filepath.Join(cache, service+"-"+costkit.OrGlobal(region)+".json")
	if _, err := os.Stat(file); err == nil {
		return file, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodGet, offer.Host+offer.Path(service, region), nil)
	if err != nil {
		return "", err
	}
	part := file + ".part"
	out, err := os.Create(part)
	if err != nil {
		return "", err
	}
	err = costkit.Stream(context.Background(), req, func(body io.Reader) error {
		_, err := io.Copy(out, body)
		return err
	})
	if closing := out.Close(); err == nil {
		err = closing
	}
	if err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return file, os.Rename(part, file)
}

func resolve(file string, rates []*costkit.Rate, held group, today string) error {
	source, err := os.Open(file)
	if err != nil {
		return err
	}
	defer source.Close()

	matched := make([][]string, len(rates))
	wanted := map[string][]int{}
	priced := map[string][]offer.Price{}
	version, err := offer.ReadOnDemand(source, offer.Sink{
		Product: func(product offer.Product) error {
			for i, rate := range rates {
				if matches(product.Attributes, rate.Query, held.region) {
					matched[i] = append(matched[i], product.SKU)
					wanted[product.SKU] = append(wanted[product.SKU], i)
				}
			}
			return nil
		},
		Price: func(price offer.Price) error {
			if _, want := wanted[price.SKU]; want {
				priced[price.SKU] = append(priced[price.SKU], price)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	for i, rate := range rates {
		slices.Sort(matched[i])
		if len(matched[i]) != 1 {
			return fmt.Errorf("%s: query %v matches %d products: %v", rate.ID, rate.Query, len(matched[i]), matched[i])
		}
		sku := matched[i][0]
		tiers, unit, err := tiersOf(sku, priced[sku])
		if err != nil {
			return fmt.Errorf("%s: %w", rate.ID, err)
		}
		if rate.Unit != unit {
			fmt.Fprintf(os.Stderr, "%s: the offer bills in %q, the card says %q\n", rate.ID, unit, rate.Unit)
		}
		rate.Tiers = tiers
		rate.Source = offer.Source(held.service, version, held.region) + "#" + sku
		rate.Verified = today
	}
	return nil
}

func tiersOf(sku string, prices []offer.Price) ([]costkit.Tier, string, error) {
	var tiers []costkit.Tier
	unit := ""
	for _, price := range prices {
		start, err := decimal.NewFromString(price.BeginRange)
		if err != nil {
			return nil, "", fmt.Errorf("sku %s: beginRange %q: %w", sku, price.BeginRange, err)
		}
		amount, err := decimal.NewFromString(price.USD)
		if err != nil {
			return nil, "", fmt.Errorf("sku %s: price %q: %w", sku, price.USD, err)
		}
		tiers = append(tiers, costkit.Tier{Start: start, Price: amount})
		unit = price.Unit
	}
	if len(tiers) == 0 {
		return nil, "", fmt.Errorf("sku %s carries no on-demand term", sku)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Start.LessThan(tiers[j].Start) })
	return tiers, unit, nil
}

func matches(attributes, query map[string]string, region string) bool {
	if region != "" && attributes["regionCode"] != region && attributes["fromRegionCode"] != region {
		return false
	}
	for key, want := range query {
		if key == costkit.QueryService || key == costkit.QueryIndex {
			continue
		}
		if attributes[key] != want {
			return false
		}
	}
	return true
}
