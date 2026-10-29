package ingest

import "slices"

const (
	Format   = 1
	Currency = "USD"
)

const (
	SourceAWSOffers  = "aws-offers"
	SourceGCPCatalog = "gcp-catalog"
	SourceStatic     = "static"
)

type Manifest struct {
	Format   int      `json:"format"`
	Currency string   `json:"currency"`
	Vendors  []Vendor `json:"vendors"`
}

type Vendor struct {
	Vendor   string    `json:"vendor"`
	Source   string    `json:"source"`
	Services []Service `json:"services"`
}

type Service struct {
	Service         string   `json:"service"`
	Regions         []string `json:"regions,omitempty"`
	ProductFamilies []string `json:"productFamilies,omitempty"`
	Where           []Filter `json:"where,omitempty"`
	Exclude         []Filter `json:"exclude,omitempty"`
	File            string   `json:"file,omitempty"`
}

type Filter struct {
	Attribute       string   `json:"attribute"`
	In              []string `json:"in"`
	ProductFamilies []string `json:"productFamilies,omitempty"`
}

func (s Service) Keeps(family string, attributes map[string]string) bool {
	if len(s.ProductFamilies) > 0 && !slices.Contains(s.ProductFamilies, family) {
		return false
	}
	for _, filter := range s.Where {
		if filter.applies(family) && !slices.Contains(filter.In, attributes[filter.Attribute]) {
			return false
		}
	}
	for _, filter := range s.Exclude {
		if filter.applies(family) && slices.Contains(filter.In, attributes[filter.Attribute]) {
			return false
		}
	}
	return true
}

func (f Filter) applies(family string) bool {
	return len(f.ProductFamilies) == 0 || slices.Contains(f.ProductFamilies, family)
}
