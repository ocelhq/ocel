package ruleset

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/aws"
	"github.com/ocelhq/ocel/pkg/pricing/cloudflare"
	"github.com/ocelhq/ocel/pkg/pricing/gcp"
	"github.com/ocelhq/ocel/pkg/pricing/ingest"
)

//go:generate go run ./generate ../ingest/manifest.json

func Vendors() []pricing.Rules {
	return []pricing.Rules{aws.Rules, gcp.Rules, cloudflare.Rules}
}

func Table() pricing.Table {
	return pricing.Combine(Vendors()...)
}

func Manifest() ingest.Manifest {
	manifest := ingest.Manifest{Format: ingest.Format, Currency: ingest.Currency}
	for _, rules := range Vendors() {
		manifest.Vendors = append(manifest.Vendors, rules.Ingest)
	}
	return manifest
}

func ManifestJSON() ([]byte, error) {
	encoded, err := json.MarshalIndent(Manifest(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
