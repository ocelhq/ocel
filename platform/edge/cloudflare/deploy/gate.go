package cloudflare

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
)

const gatePath = "/gate"

func (p *cloudflare) publishGate(ctx context.Context, publication edge.GatePublication) error {
	state := publication.State
	if state.Secret == "" {
		return fmt.Errorf("the %s edge has no releases-store identity for the %s stack of %s to publish its gate with; deploy it first", Kind, state.Tier, state.Slug)
	}
	return p.putGate(ctx, state, publication.Gate)
}

func (p *cloudflare) putGate(ctx context.Context, state edge.StackState, published *gate.Gate) error {
	if published == nil {
		_, err := p.storeRequest(ctx, state, http.MethodDelete, gatePath, nil, nil)
		return err
	}
	_, err := p.storeRequest(ctx, state, http.MethodPut, gatePath, published, nil)
	return err
}
