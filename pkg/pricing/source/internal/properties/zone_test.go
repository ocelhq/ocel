package properties_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

func TestAZoneDropsItsSuffixToNameItsRegion(t *testing.T) {
	for in, want := range map[string]string{
		"europe-west1-b": "europe-west1",
		"us-central1-a":  "us-central1",
		"us-east-1a":     "us-east-1",
		"eu-west-2":      "eu-west-2",
		"europe-west1":   "europe-west1",
	} {
		if got := properties.ZoneRegion(in); got != want {
			t.Errorf("ZoneRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestALocationNamesARegionUnlessItIsAMultiRegion(t *testing.T) {
	for _, c := range []struct {
		location, region string
		known            bool
	}{
		{"europe-west1", "europe-west1", true},
		{"europe-west1-b", "europe-west1", true},
		{"EU", "", false},
		{"us", "", false},
		{"ASIA", "", false},
		{"", "", false},
	} {
		region, known := properties.LocationRegion(c.location)
		if region != c.region || known != c.known {
			t.Errorf("LocationRegion(%q) = (%q, %v), want (%q, %v)", c.location, region, known, c.region, c.known)
		}
	}
}
