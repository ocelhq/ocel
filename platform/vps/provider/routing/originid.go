package routing

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
)

func (t *Table) EnsureOriginID(ctx context.Context) (string, error) {
	minted := naming.NewRandomTag()
	for rewrites := 1; ; rewrites++ {
		pair, err := t.CurrentTable(ctx)
		if err != nil {
			return "", err
		}
		table, err := ReadRoutingTable(pair.Table)
		if err != nil {
			return "", err
		}
		if table.OriginID != "" {
			return table.OriginID, nil
		}
		table.OriginID = minted
		written, err := WriteRoutingTable(table)
		if err != nil {
			return "", err
		}
		_, failedPlace, err := t.writePair(ctx, pair.Digest(), Pair{Table: written, Config: pair.Config, Origins: pair.Origins}, false)
		if err == nil {
			return minted, failedPlace
		}
		if !Moved(err) || rewrites >= Rewrites {
			return "", err
		}
	}
}
