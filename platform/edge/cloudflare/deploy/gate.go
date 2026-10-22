package cloudflare

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const gatePath = "/gate"

func (p *cloudflare) publishGate(ctx context.Context, publication edge.GatePublication) error {
	state := publication.State
	if state.Secret == "" {
		return fmt.Errorf("the %s edge has no releases-store identity for the %s stack of %s to publish its gate with; deploy it first", Kind, state.Tier, state.Slug)
	}
	if _, err := p.readTierAccount(ctx, state.Tier, fmt.Sprintf("publish the %s gate of %s", state.Tier, state.Slug)); err != nil {
		return err
	}
	return p.putGate(ctx, state, publication.Gate)
}

func (p *cloudflare) putGate(ctx context.Context, state edge.StackState, published *gate.Gate) error {
	method, body := http.MethodPut, any(published)
	if published == nil {
		method, body = http.MethodDelete, nil
	}
	res, err := p.store.request(ctx, state, method, gatePath, body, nil)
	if res == nil || res.StatusCode != http.StatusNotFound {
		return err
	}
	if published == nil {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"the releases-store worker of this account's %s bootstrap answers no gate route, so it cannot hold the gate that protects the %s tier of %s.\nRe-run `%s` to install this build's, then try again",
		state.Tier, state.Tier, state.Slug, provider.BootstrapCommand(state.Tier))
}
