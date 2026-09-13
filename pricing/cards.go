package pricing

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	aws "github.com/ocelhq/ocel/platform/aws/provider/cost"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

func Cards() (*costkit.Card, error) {
	held := make([]*costkit.Card, 0, 3)
	for _, load := range []func() (*costkit.Card, error){aws.Card, gcp.Card, cloudflare.Card} {
		card, err := load()
		if err != nil {
			return nil, err
		}
		held = append(held, card)
	}
	return costkit.Merge(held[0], held[1:]...), nil
}

func Table() costkit.Table {
	return costkit.Tables(aws.Table, gcp.Table, cloudflare.Table)
}

func Notes() []string {
	notes := make([]string, 0, len(aws.Notes)+len(gcp.Notes)+len(cloudflare.Notes))
	for _, vendor := range [][]string{aws.Notes, gcp.Notes, cloudflare.Notes} {
		notes = append(notes, vendor...)
	}
	return notes
}
