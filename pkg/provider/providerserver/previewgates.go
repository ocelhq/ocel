package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func readPreviewGates(ctx context.Context, p provider.Provider) (map[string]previewgate.Gate, error) {
	return stackrecords.ReadPreviewGates(ctx, p.KeyValues(), p.Cipher())
}

func readPreviewGate(ctx context.Context, p provider.Provider, slug string) (*previewgate.Gate, error) {
	gates, err := readPreviewGates(ctx, p)
	if err != nil {
		return nil, err
	}
	gate, recorded := gates[slug]
	if !recorded {
		return nil, nil
	}
	return &gate, nil
}

func (r *deployRun) readProjectPreviewGate(ctx context.Context) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	gate, err := readPreviewGate(ctx, r.provider, r.spec.Slug)
	if err != nil {
		return err
	}
	if gate == nil {
		return nil
	}
	if !r.front.Facts().GatesPreviews {
		return refusal.Refuse(refusal.CodeUnsupported,
			"%s has a preview gate recorded, and the %s edge does not run the preview gate yet, so it would serve this preview to anyone: deploy the preview through an edge that gates previews",
			r.spec.Slug, r.front.Kind())
	}
	r.previewGate = gate
	return nil
}
