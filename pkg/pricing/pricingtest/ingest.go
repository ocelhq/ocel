package pricingtest

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/pricing/ingest"
)

func (c *Card) MissedBy(vendor ingest.Vendor) []string {
	services := map[string]ingest.Service{}
	for _, service := range vendor.Services {
		services[service.Service] = service
	}
	var missed []string
	for _, queries := range c.Lookups() {
		for _, query := range queries {
			if query.Vendor != vendor.Vendor {
				continue
			}
			service, ok := services[query.Service]
			if !ok {
				missed = append(missed, fmt.Sprintf("%s ingests no service %s", vendor.Vendor, query.Service))
				continue
			}
			for _, p := range c.products {
				if p.vendor == query.Vendor && p.Service == query.Service && p.matches(query) &&
					!service.Keeps(p.ProductFamily, p.Attributes) {
					missed = append(missed, fmt.Sprintf("%s %s drops sku %s", vendor.Vendor, query.Service, p.SKU))
				}
			}
		}
	}
	return missed
}
