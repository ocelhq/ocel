package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/platform/aws/provider/cost/offer"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost/catalog"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

const (
	queryService = "service"
	queryIndex   = "index"
	querySource  = "static"
	globalIndex  = "global"
)

type AWSTarget struct {
	Service string
	Region  string
}

func AWSTargets(card *costkit.Card) []AWSTarget {
	var targets []AWSTarget
	for _, rate := range card.Rates {
		if !strings.HasPrefix(rate.ID, postgres.VendorAWS+"/") || rate.Query == nil || rate.Query[querySource] != "" {
			continue
		}
		region := rate.Region
		if rate.Query[queryIndex] == globalIndex {
			region = ""
		}
		target := AWSTarget{Service: rate.Query[queryService], Region: region}
		if target.Service != "" && !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	slices.SortFunc(targets, func(a, b AWSTarget) int {
		if a.Service != b.Service {
			return strings.Compare(a.Service, b.Service)
		}
		return strings.Compare(a.Region, b.Region)
	})
	return targets
}

func GCPServices(card *costkit.Card) []string {
	var services []string
	for _, rate := range card.Rates {
		if !strings.HasPrefix(rate.ID, postgres.VendorGCP+"/") || rate.Query == nil {
			continue
		}
		if service := rate.Query[queryService]; service != "" && !slices.Contains(services, service) {
			services = append(services, service)
		}
	}
	slices.Sort(services)
	return services
}

type AWS struct {
	Store *postgres.Store
	Host  string
	Now   func() time.Time
}

func (a AWS) Run(ctx context.Context, service, region string) error {
	host := a.Host
	if host == "" {
		host = offer.Host
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+offer.Path(service, region), nil)
	if err != nil {
		return err
	}
	held, err := a.Store.IngestAWS(ctx, service, region)
	if err != nil {
		return err
	}
	defer held.Close()

	version := ""
	err = costkit.Stream(ctx, req, func(body io.Reader) error {
		read, err := offer.ReadOnDemand(body, offer.Sink{
			Product: func(product offer.Product) error {
				return held.Product(postgres.AWSProduct{SKU: product.SKU, Attributes: product.Attributes})
			},
			Price: func(price offer.Price) error {
				if price.USD == "" {
					return nil
				}
				start, err := decimal.NewFromString(price.BeginRange)
				if err != nil {
					return fmt.Errorf("sku %s: beginRange %q: %w", price.SKU, price.BeginRange, err)
				}
				amount, err := decimal.NewFromString(price.USD)
				if err != nil {
					return fmt.Errorf("sku %s: price %q: %w", price.SKU, price.USD, err)
				}
				return held.Price(postgres.AWSPrice{
					SKU: price.SKU, Unit: price.Unit, BeginRange: start, EndRange: price.EndRange, Price: amount,
				})
			},
		})
		version = read
		return err
	})
	if err != nil {
		return fmt.Errorf("%s in %s: %w", service, orGlobal(region), err)
	}
	return held.Commit(version, a.now())
}

func (a AWS) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now().UTC()
}

type Result struct {
	Skipped int
}

type GCP struct {
	Store *postgres.Store
	Host  string
	Key   string
	Log   *slog.Logger
	Now   func() time.Time
}

func (g GCP) Run(ctx context.Context, service string) (Result, error) {
	host := g.Host
	if host == "" {
		host = catalog.Host
	}
	skus, err := catalog.List(ctx, host, service, g.Key)
	if err != nil {
		return Result{}, err
	}
	held, err := g.Store.IngestGCP(ctx, service)
	if err != nil {
		return Result{}, err
	}
	defer held.Close()

	var result Result
	for _, sku := range skus {
		tiers, _, err := catalog.Tiers(sku)
		if err != nil {
			result.Skipped++
			g.log().Warn("this sku stays on the embedded card", "vendor", postgres.VendorGCP, "service", service, "sku", sku.SKUID, "reason", err.Error())
			continue
		}
		if err := held.SKU(postgres.GCPSKU{
			ID: sku.SKUID, Service: service, Description: sku.Description,
			Category: sku.Category, Regions: sku.ServiceRegions, Tiers: tiers,
		}); err != nil {
			return result, err
		}
	}
	at := g.now()
	return result, held.Commit(at.UTC().Format(time.RFC3339), at)
}

func (g GCP) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (g GCP) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now().UTC()
}

func orGlobal(region string) string {
	if region == "" {
		return globalIndex
	}
	return region
}
