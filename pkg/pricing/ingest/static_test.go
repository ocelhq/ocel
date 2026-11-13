package ingest_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/ingest"
)

func TestEveryStaticCatalogIsValid(t *testing.T) {
	files, err := os.ReadDir("static")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		t.Run(file.Name(), func(t *testing.T) {
			raw, err := os.ReadFile("static/" + file.Name())
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := ingest.ReadStatic(raw)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.Vendor+".json" != file.Name() {
				t.Errorf("vendor = %s, want the file named after it", catalog.Vendor)
			}
		})
	}
}

const product = `{"service": "workers", "sku": %q, "region": "", "productFamily": "Workers", "attributes": {"metric": "requests"},
	"purchaseOption": "on_demand", "unit": "requests", "tiers": %s, "source": %q, "verified": "2026-09-13"}`

func static(products ...string) string {
	return `{"format": 1, "vendor": "cloudflare", "version": "2026-09-13", "currency": "USD", "products": [` + strings.Join(products, ",") + `]}`
}

func entry(sku, tiers, source string) string {
	return fmt.Sprintf(product, sku, tiers, source)
}

func TestAStaticCatalogRefusesWhatTheConsoleCouldNotServe(t *testing.T) {
	good := entry("workers-requests", `[{"start": "0", "price": "0"}, {"start": "10000000", "price": "0.0000003"}]`, "https://example.test")
	if _, err := ingest.ReadStatic([]byte(static(good))); err != nil {
		t.Fatalf("ReadStatic() refused a valid catalog: %v", err)
	}
	for name, raw := range map[string]string{
		"format":           strings.Replace(static(good), `"format": 1`, `"format": 2`, 1),
		"currency":         strings.Replace(static(good), `"USD"`, `"EUR"`, 1),
		"no version":       strings.Replace(static(good), `"2026-09-13", "currency"`, `"", "currency"`, 1),
		"duplicate sku":    static(good, good),
		"tier not at zero": static(entry("a", `[{"start": "1", "price": "1"}]`, "s")),
		"no tiers":         static(entry("a", `[]`, "s")),
		"descending tiers": static(entry("a", `[{"start": "0", "price": "1"}, {"start": "9", "price": "1"}, {"start": "5", "price": "1"}]`, "s")),
		"negative price":   static(entry("a", `[{"start": "0", "price": "-1"}]`, "s")),
		"not a decimal":    static(entry("a", `[{"start": "0", "price": "cheap"}]`, "s")),
		"no source":        static(entry("a", `[{"start": "0", "price": "1"}]`, "")),
		"unknown field":    strings.Replace(static(good), `"format": 1`, `"format": 1, "extra": true`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ingest.ReadStatic([]byte(raw)); err == nil {
				t.Fatal("ReadStatic() accepted it")
			}
		})
	}
}
