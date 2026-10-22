package routing

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func (t *Table) EnsureOriginID(ctx context.Context) (string, error) {
	current, err := t.Read(ctx)
	if err != nil {
		return "", err
	}
	if current.OriginID != "" {
		return current.OriginID, nil
	}
	minted := naming.NewRandomTag()
	var held string
	err = t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if state.OriginID == "" {
			state.OriginID = minted
		}
		held = state.OriginID
		return state, nil
	})
	return held, err
}
