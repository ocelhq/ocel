package providerserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var previewGatesAssociatedData = seal.AssociatedData{{Name: "record", Value: "preview-gates"}}

func readPreviewGates(ctx context.Context, p provider.Provider) (map[string]previewgate.Gate, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.KeyValues(), stackrecords.NewPreviewGatesRecordKey())
	if err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	if len(recorded.Value) == 0 {
		return nil, nil
	}
	var sealed []byte
	if err := json.Unmarshal(recorded.Value, &sealed); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	plaintext, err := p.Cipher().Open(ctx, environment.TierPreview, previewGatesAssociatedData, sealed)
	if err != nil {
		return nil, fmt.Errorf("open the keys that gate previews: %w", err)
	}
	var gates map[string]previewgate.Gate
	if err := json.Unmarshal(plaintext, &gates); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	return gates, nil
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
		if r.provider.Facts().GatesPreviewsAtOrigin && !cachesInFrontOfOrigin(r.front.Facts()) {
			r.previewGate = gate
			return nil
		}
		return refusal.Refuse(refusal.CodeUnsupported,
			"%s has a preview gate recorded, and the %s edge does not run the preview gate yet, so it would serve this preview to anyone: deploy the preview through an edge that gates previews",
			r.spec.Slug, r.front.Kind())
	}
	if !r.front.Facts().RunsCode && !r.provider.Facts().AnswersPreviewLogin {
		return refusal.Refuse(refusal.CodeUnsupported,
			"%s has a preview gate recorded, and the %s edge hands the preview login to the origin, which on %s does not answer it yet, so the password would reach the app: deploy the preview through an edge that runs code",
			r.spec.Slug, r.front.Kind(), r.provider.Facts().Vendor)
	}
	r.previewGate = gate
	return nil
}

func (r *deployRun) appPreviewGate() *previewgate.Gate {
	if r.front.Facts().RunsCode {
		return nil
	}
	return r.previewGate
}

func cachesInFrontOfOrigin(facts edge.Facts) bool {
	return facts.CachesResponses || facts.ProxiesRecords || facts.RunsCode
}
