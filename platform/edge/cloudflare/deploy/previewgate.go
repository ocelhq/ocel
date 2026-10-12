package cloudflare

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
)

const previewGatePath = "/preview-gate"

func (p *cloudflare) publishPreviewGate(ctx context.Context, publication edge.PreviewGatePublication) error {
	state := publication.State
	if state.Tier != environment.TierPreview {
		return fmt.Errorf("the %s edge gates previews only, and %s is a %s stack", Kind, state.Slug, state.Tier)
	}
	if state.Secret == "" {
		return fmt.Errorf("the %s edge has no releases-store identity for %s to publish its preview gate with; deploy the project's previews first", Kind, state.Slug)
	}
	return p.putPreviewGate(ctx, state, publication.Gate)
}

func (p *cloudflare) putPreviewGate(ctx context.Context, state edge.StackState, gate *previewgate.Gate) error {
	if gate == nil {
		_, err := p.storeRequest(ctx, state, http.MethodDelete, previewGatePath, nil, nil)
		return err
	}
	_, err := p.storeRequest(ctx, state, http.MethodPut, previewGatePath, gate, nil)
	return err
}
