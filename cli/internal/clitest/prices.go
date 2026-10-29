package clitest

import (
	"fmt"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
)

const (
	FakePriceVendor         = "fake"
	FakePriceCatalogVersion = "20261028000000"
)

func fakePriceQuery(service, region string) pricing.Query {
	return pricing.Query{Vendor: FakePriceVendor, Service: service, Region: region, Attributes: map[string]string{"usagetype": service}}
}

var (
	fakeRequestsBand = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	fakeStorageBand  = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
)

var FakePriceTable = pricing.Table{
	"fake_function": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", Price: fakePriceQuery("Requests", r.Region()), Quantity: r.Usage("monthly_requests", fakeRequestsBand), UsageBased: true})
	},
	"fake_container": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Container", Unit: "hours", Price: fakePriceQuery("ContainerHours", r.Region()), Quantity: pricing.MonthlyHours})
	},
	"fake_postgres": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Database", Unit: "hours", Price: fakePriceQuery("PostgresHours", r.Region()), Quantity: pricing.MonthlyHours})
	},
	"fake_bucket": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", Price: fakePriceQuery("Storage", r.Region()), Quantity: r.Usage("storage_gb", fakeStorageBand), UsageBased: true})
	},
}

func fakeProduct(service, price string) string {
	return fmt.Sprintf(`{"service":%[1]q,"sku":"FAKE-%[1]s","region":"","productFamily":"","attributes":{"usagetype":%[1]q},"purchaseOption":"on_demand","unit":%[1]q,"tiers":[{"start":"0","price":%[2]q}],"source":"https://fake.example/pricing","verified":"2026-10-28"}`, service, price)
}

var fakePriceCatalog = fmt.Sprintf(`{"format":1,"vendor":%q,"version":%q,"currency":"USD","products":[%s,%s,%s,%s]}`,
	FakePriceVendor, FakePriceCatalogVersion,
	fakeProduct("Requests", "0.000001"),
	fakeProduct("ContainerHours", "0.01"),
	fakeProduct("PostgresHours", "0.02"),
	fakeProduct("Storage", "0.01"),
)

func FakePriceCard(t testing.TB) *pricingtest.Card {
	t.Helper()
	card, err := pricingtest.NewCard([]byte(fakePriceCatalog))
	if err != nil {
		t.Fatalf("read the fake price catalog: %v", err)
	}
	return card
}
