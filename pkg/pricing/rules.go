package pricing

import "github.com/ocelhq/ocel/pkg/pricing/ingest"

type Rules struct {
	Table  Table
	Notes  []string
	Ingest ingest.Vendor
}
